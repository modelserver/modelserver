import { useMemo, useState } from "react";
import { useModels } from "@/api/models";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";
import { Check, ChevronsUpDown, X } from "lucide-react";
import { cn } from "@/lib/utils";

// ModelMultiSelect lets the caller pick zero or more canonical model names
// from the catalog. Aliases resolve to their canonical name on pick so
// downstream admin writes never store the alias itself. Options are grouped
// visually: active models on top, then disabled models for reference.
export function ModelMultiSelect({
  value,
  onChange,
  placeholder = "Select models...",
  disabled,
  allowCustom = false,
}: {
  value: string[];
  onChange: (next: string[]) => void;
  placeholder?: string;
  disabled?: boolean;
  // allowCustom=true lets the user type a name that's not in the catalog and
  // add it anyway. Used by the UpstreamsPage model_map input where the
  // right-hand side is the upstream-side identifier (not a catalog name).
  allowCustom?: boolean;
}) {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const { data, isLoading } = useModels();
  const rows = data?.data ?? [];

  const [active, dimmed] = useMemo(() => {
    const matching = rows.filter((m) => {
      if (!query) return true;
      const q = query.toLowerCase();
      if (m.name.includes(q)) return true;
      if (m.display_name.toLowerCase().includes(q)) return true;
      return m.aliases?.some((a) => a.toLowerCase().includes(q));
    });
    return [
      matching.filter((m) => m.status === "active"),
      matching.filter((m) => m.status !== "active"),
    ];
  }, [rows, query]);

  const selected = new Set(value);

  function toggle(name: string) {
    const next = new Set(selected);
    if (next.has(name)) next.delete(name);
    else next.add(name);
    onChange(Array.from(next));
  }

  function remove(name: string) {
    onChange(value.filter((v) => v !== name));
  }

  function addCustom() {
    const v = query.trim().toLowerCase();
    if (!v || selected.has(v)) return;
    onChange([...value, v]);
    setQuery("");
  }

  return (
    <div className="space-y-2">
      {value.length > 0 && (
        <div className="flex flex-wrap gap-1">
          {value.map((v) => {
            const m = rows.find((r) => r.name === v);
            return (
              <Badge
                key={v}
                variant={m ? "secondary" : "outline"}
                className="gap-1"
              >
                <code className="text-xs">{v}</code>
                {!m && (
                  <span className="text-[10px] text-muted-foreground">(not in catalog)</span>
                )}
                <button
                  type="button"
                  disabled={disabled}
                  onClick={() => remove(v)}
                  className="hover:text-destructive-foreground"
                  aria-label={`Remove ${v}`}
                >
                  <X className="h-3 w-3" />
                </button>
              </Badge>
            );
          })}
        </div>
      )}
      <Popover open={open} onOpenChange={setOpen}>
        <PopoverTrigger
          render={
            <Button
              variant="outline"
              role="combobox"
              disabled={disabled}
              className="w-full justify-between font-normal"
            />
          }
        >
          <span className="text-muted-foreground">{placeholder}</span>
          <ChevronsUpDown className="ml-2 h-4 w-4 shrink-0 opacity-50" />
        </PopoverTrigger>
        <PopoverContent className="w-[360px] p-2" align="start">
          <div className="space-y-2">
            <Input
              autoFocus
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && allowCustom) {
                  e.preventDefault();
                  addCustom();
                }
              }}
              placeholder="Search by name or alias..."
              className="h-8"
            />
            <div className="max-h-72 overflow-y-auto">
              {isLoading && (
                <div className="p-2 text-xs text-muted-foreground">Loading...</div>
              )}
              {!isLoading && active.length === 0 && dimmed.length === 0 && (
                <div className="p-2 text-xs text-muted-foreground">
                  No catalog entries match "{query}".
                  {allowCustom && (
                    <Button
                      variant="link"
                      size="sm"
                      className="px-1 text-xs"
                      onClick={addCustom}
                    >
                      Add "{query}" anyway
                    </Button>
                  )}
                </div>
              )}
              {active.map((m) => (
                <OptionRow
                  key={m.name}
                  selected={selected.has(m.name)}
                  onClick={() => toggle(m.name)}
                  name={m.name}
                  displayName={m.display_name}
                  aliases={m.aliases}
                />
              ))}
              {dimmed.length > 0 && (
                <div className="mt-2 border-t pt-2">
                  <div className="px-2 pb-1 text-[10px] uppercase tracking-wider text-muted-foreground">
                    Disabled
                  </div>
                  {dimmed.map((m) => (
                    <OptionRow
                      key={m.name}
                      selected={selected.has(m.name)}
                      onClick={() => toggle(m.name)}
                      name={m.name}
                      displayName={m.display_name}
                      aliases={m.aliases}
                      muted
                    />
                  ))}
                </div>
              )}
            </div>
          </div>
        </PopoverContent>
      </Popover>
    </div>
  );
}

function OptionRow({
  name,
  displayName,
  aliases,
  selected,
  onClick,
  muted,
}: {
  name: string;
  displayName: string;
  aliases?: string[];
  selected: boolean;
  onClick: () => void;
  muted?: boolean;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "flex w-full items-start gap-2 rounded px-2 py-1.5 text-left text-sm hover:bg-accent",
        muted && "text-muted-foreground",
      )}
    >
      <Check
        className={cn(
          "mt-0.5 h-4 w-4 shrink-0",
          selected ? "opacity-100" : "opacity-0",
        )}
      />
      <div className="flex min-w-0 flex-col">
        <code className="truncate text-xs">{name}</code>
        <span className="truncate text-[11px] text-muted-foreground">
          {displayName}
          {aliases && aliases.length > 0 && ` · aliases: ${aliases.join(", ")}`}
        </span>
      </div>
    </button>
  );
}

// ModelSingleSelect is a thin wrapper for pages that pick exactly one model
// (e.g. the left-hand side of an upstream's ModelMap row).
export function ModelSingleSelect({
  value,
  onChange,
  placeholder,
  disabled,
  allowCustom,
}: {
  value: string;
  onChange: (next: string) => void;
  placeholder?: string;
  disabled?: boolean;
  allowCustom?: boolean;
}) {
  return (
    <ModelMultiSelect
      value={value ? [value] : []}
      onChange={(arr) => onChange(arr[arr.length - 1] ?? "")}
      placeholder={placeholder}
      disabled={disabled}
      allowCustom={allowCustom}
    />
  );
}
