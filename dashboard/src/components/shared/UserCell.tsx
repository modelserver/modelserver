import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";

interface UserCellProps {
  nickname?: string;
  picture?: string;
  userId?: string;
}

export function UserCell({ nickname, picture, userId }: UserCellProps) {
  if (!nickname && !userId) {
    return <span className="text-muted-foreground">-</span>;
  }
  const displayName = nickname || `${userId!.slice(0, 8)}…`;
  const initials = (nickname || userId || "?").slice(0, 2).toUpperCase();
  return (
    <div className="flex items-center gap-2">
      <Avatar size="sm">
        {picture && <AvatarImage src={picture} alt={displayName} />}
        <AvatarFallback>{initials}</AvatarFallback>
      </Avatar>
      <span className="truncate max-w-[12rem]">{displayName}</span>
    </div>
  );
}
