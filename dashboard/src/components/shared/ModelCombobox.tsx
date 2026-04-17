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
import type { ModelListRow } from "@/api/types";

// Shared trigger button + popover shell used by both the multi- and
// single-select wrappers below. `renderRows` provides the actual option list
// so the two variants can implement their own pick semantics while sharing
// the search box, disabled grouping, and styling.
function ComboboxShell({
  placeholder,
  disabled,
  triggerLabel,
  renderRows,
}: {
  placeholder: string;
  disabled?: boolean;
  triggerLabel: React.ReactNode;
  renderRows: (args: {
    query: string;
    active: ModelListRow[];
    dimmed: ModelListRow[];
    isLoading: boolean;
    close: () => void;
  }) => React.ReactNode;
}) {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const { data, isLoading } = useModels();
  const rows = data?.data ?? [];

  const [active, dimmed] = useMemo(() => {
    const matching = rows.filter((m) => {
      if (!query) return true;
      const q = query.toLowerCase();
      if (m.name.toLowerCase().includes(q)) return true;
      if (m.display_name.toLowerCase().includes(q)) return true;
      return m.aliases?.some((a) => a.toLowerCase().includes(q));
    });
    return [
      matching.filter((m) => m.status === "active"),
      matching.filter((m) => m.status !== "active"),
    ] as const;
  }, [rows, query]);

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger
        render={
          <Button
            type="button"
            variant="outline"
            role="combobox"
            disabled={disabled}
            className="w-full justify-between font-normal"
          />
        }
      >
        {triggerLabel || (
          <span className="text-muted-foreground">{placeholder}</span>
        )}
        <ChevronsUpDown className="ml-2 h-4 w-4 shrink-0 opacity-50" />
      </PopoverTrigger>
      <PopoverContent className="w-[360px] p-2" align="start">
        <div className="space-y-2">
          <Input
            autoFocus
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Search by name or alias..."
            className="h-8"
          />
          <div className="max-h-72 overflow-y-auto">
            {isLoading && (
              <div className="p-2 text-xs text-muted-foreground">Loading...</div>
            )}
            {!isLoading &&
              renderRows({
                query,
                active,
                dimmed,
                isLoading,
                close: () => setOpen(false),
              })}
          </div>
        </div>
      </PopoverContent>
    </Popover>
  );
}

// ModelMultiSelect lets the caller pick zero or more canonical model names
// from the catalog. Options are grouped visually: active models on top,
// then disabled models for reference. Clicking toggles the pick and leaves
// the popover open so several can be chosen in one session.
export function ModelMultiSelect({
  value,
  onChange,
  placeholder = "Select models...",
  disabled,
}: {
  value: string[];
  onChange: (next: string[]) => void;
  placeholder?: string;
  disabled?: boolean;
}) {
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

  return (
    <div className="space-y-2">
      {value.length > 0 && (
        <div className="flex flex-wrap gap-1">
          {value.map((v) => (
            <Badge key={v} variant="secondary" className="gap-1">
              <code className="text-xs">{v}</code>
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
          ))}
        </div>
      )}
      <ComboboxShell
        placeholder={placeholder}
        disabled={disabled}
        triggerLabel={null}
        renderRows={({ query, active, dimmed }) => (
          <>
            {active.length === 0 && dimmed.length === 0 && (
              <div className="p-2 text-xs text-muted-foreground">
                No catalog entries match "{query}".
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
          </>
        )}
      />
    </div>
  );
}

// ModelSingleSelect picks exactly one catalog model. Clicking a row replaces
// the current selection (unlike toggle-style multi-select) and closes the
// popover immediately so the caller's form doesn't sit open waiting for a
// second click.
export function ModelSingleSelect({
  value,
  onChange,
  placeholder = "Select a model...",
  disabled,
}: {
  value: string;
  onChange: (next: string) => void;
  placeholder?: string;
  disabled?: boolean;
}) {
  return (
    <ComboboxShell
      placeholder={placeholder}
      disabled={disabled}
      triggerLabel={
        value ? (
          <code className="truncate text-xs">{value}</code>
        ) : null
      }
      renderRows={({ query, active, dimmed, close }) => (
        <>
          {active.length === 0 && dimmed.length === 0 && (
            <div className="p-2 text-xs text-muted-foreground">
              No catalog entries match "{query}".
            </div>
          )}
          {active.map((m) => (
            <OptionRow
              key={m.name}
              selected={value === m.name}
              onClick={() => {
                onChange(m.name);
                close();
              }}
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
                  selected={value === m.name}
                  onClick={() => {
                    onChange(m.name);
                    close();
                  }}
                  name={m.name}
                  displayName={m.display_name}
                  aliases={m.aliases}
                  muted
                />
              ))}
            </div>
          )}
        </>
      )}
    />
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
