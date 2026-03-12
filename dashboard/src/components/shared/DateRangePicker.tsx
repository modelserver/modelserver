import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

interface DateRangePickerProps {
  since: string;
  until: string;
  onSinceChange: (val: string) => void;
  onUntilChange: (val: string) => void;
}

export function DateRangePicker({
  since,
  until,
  onSinceChange,
  onUntilChange,
}: DateRangePickerProps) {
  return (
    <div className="flex items-end gap-3">
      <div className="space-y-1">
        <Label className="text-xs">From</Label>
        <Input
          type="date"
          value={since}
          onChange={(e) => onSinceChange(e.target.value)}
          className="w-40"
        />
      </div>
      <div className="space-y-1">
        <Label className="text-xs">To</Label>
        <Input
          type="date"
          value={until}
          onChange={(e) => onUntilChange(e.target.value)}
          className="w-40"
        />
      </div>
    </div>
  );
}
