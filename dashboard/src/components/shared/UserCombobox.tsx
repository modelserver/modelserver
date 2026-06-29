import { useMemo, useState } from "react";
import { useAllUsersCompact, type UserCompact } from "@/api/users";
import { Avatar, AvatarImage, AvatarFallback } from "@/components/ui/avatar";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";
import { Check, ChevronsUpDown, X } from "lucide-react";
import { cn } from "@/lib/utils";

interface UserComboboxProps {
  value: string | null;
  onChange: (userId: string | null) => void;
  placeholder?: string;
  className?: string;
}

// UserCombobox renders a single-select user picker showing avatar +
// nickname + email per row. Substring match runs on the client over
// the pre-fetched useAllUsersCompact() result. Selecting null clears
// the selection.
export function UserCombobox({
  value,
  onChange,
  placeholder = "Filter by owner",
  className,
}: UserComboboxProps) {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const { data, isLoading } = useAllUsersCompact();
  const users = data?.data ?? [];

  const selected = useMemo(
    () => (value ? users.find((u) => u.id === value) ?? null : null),
    [users, value],
  );

  const filtered = useMemo(() => {
    if (!query) return users;
    const q = query.toLowerCase();
    return users.filter(
      (u) =>
        u.nickname?.toLowerCase().includes(q) ||
        u.email?.toLowerCase().includes(q) ||
        u.id.toLowerCase().includes(q),
    );
  }, [users, query]);

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger
        render={
          <Button
            type="button"
            variant="outline"
            role="combobox"
            className={cn("justify-between font-normal", className)}
          />
        }
      >
        {selected ? (
          <UserRow user={selected} compact />
        ) : (
          <span className="text-muted-foreground">{placeholder}</span>
        )}
        <span className="ml-2 flex items-center gap-1">
          {selected ? (
            <X
              className="h-3 w-3 opacity-60 hover:opacity-100"
              onClick={(e) => {
                e.stopPropagation();
                onChange(null);
              }}
            />
          ) : null}
          <ChevronsUpDown className="h-4 w-4 shrink-0 opacity-50" />
        </span>
      </PopoverTrigger>
      <PopoverContent className="p-0 w-[320px]" align="start">
        <div className="border-b p-2">
          <Input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Search by name or email…"
            autoFocus
          />
        </div>
        <div className="max-h-72 overflow-y-auto">
          {isLoading ? (
            <div className="p-3 text-xs text-muted-foreground">Loading users…</div>
          ) : filtered.length === 0 ? (
            <div className="p-3 text-xs text-muted-foreground">No users found.</div>
          ) : (
            filtered.map((u) => {
              const isSelected = value === u.id;
              return (
                <button
                  key={u.id}
                  type="button"
                  className={cn(
                    "flex w-full items-center gap-2 px-2 py-1.5 text-left hover:bg-accent",
                    isSelected && "bg-accent",
                  )}
                  onClick={() => {
                    onChange(u.id);
                    setOpen(false);
                  }}
                >
                  <UserRow user={u} />
                  {isSelected ? (
                    <Check className="ml-auto h-4 w-4" />
                  ) : null}
                </button>
              );
            })
          )}
        </div>
      </PopoverContent>
    </Popover>
  );
}

function UserRow({ user, compact = false }: { user: UserCompact; compact?: boolean }) {
  const fallback = (user.nickname ?? user.id).slice(0, 2).toUpperCase();
  return (
    <div className={cn("flex items-center gap-2 min-w-0", compact && "max-w-[240px]")}>
      <Avatar className="h-5 w-5 shrink-0">
        {user.picture ? <AvatarImage src={user.picture} /> : null}
        <AvatarFallback className="text-[10px]">{fallback}</AvatarFallback>
      </Avatar>
      <span className="text-sm truncate">
        {user.nickname || user.id.slice(0, 8)}
      </span>
      {user.email ? (
        <span className="text-xs text-muted-foreground truncate">{user.email}</span>
      ) : null}
    </div>
  );
}
