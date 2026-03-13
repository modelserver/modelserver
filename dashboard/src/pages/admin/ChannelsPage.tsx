import { useState, useMemo } from "react";
import { useChannels, useCreateChannel, useUpdateChannel, useDeleteChannel, useChannelStats, useTestChannel } from "@/api/channels";
import { PageHeader } from "@/components/layout/PageHeader";
import { DataTable, type Column } from "@/components/shared/DataTable";
import { StatusBadge } from "@/components/shared/StatusBadge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Card, CardContent } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogFooter,
} from "@/components/ui/dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import type { Channel, ChannelUsageSummary } from "@/api/types";
import { Plus, MoreHorizontal, Zap, Loader2 } from "lucide-react";
import { toast } from "sonner";

export function ChannelsPage() {
  const { data, isLoading } = useChannels();
  const { data: statsData } = useChannelStats();
  const createChannel = useCreateChannel();
  const updateChannel = useUpdateChannel();
  const deleteChannel = useDeleteChannel();
  const testChannel = useTestChannel();

  const [showCreate, setShowCreate] = useState(false);
  const [form, setForm] = useState({
    provider: "anthropic",
    name: "",
    base_url: "",
    api_key: "",
    supported_models: "",
    weight: "1",
    selection_priority: "0",
    max_concurrent: "10",
    test_model: "",
  });

  const channels = data?.data ?? [];
  const statsMap = useMemo(() => {
    const m = new Map<string, ChannelUsageSummary>();
    for (const s of statsData?.data ?? []) {
      m.set(s.channel_id, s);
    }
    return m;
  }, [statsData]);

  function updateForm(key: string, value: string) {
    setForm((prev) => ({ ...prev, [key]: value }));
  }

  async function handleCreate() {
    await createChannel.mutateAsync({
      provider: form.provider,
      name: form.name,
      base_url: form.base_url,
      api_key: form.api_key,
      supported_models: form.supported_models
        .split(",")
        .map((s) => s.trim())
        .filter(Boolean),
      weight: Number(form.weight) || 1,
      selection_priority: Number(form.selection_priority) || 0,
      max_concurrent: Number(form.max_concurrent) || 10,
      test_model: form.test_model || undefined,
    });
    setShowCreate(false);
    setForm({
      provider: "anthropic",
      name: "",
      base_url: "",
      api_key: "",
      supported_models: "",
      weight: "1",
      selection_priority: "0",
      max_concurrent: "10",
      test_model: "",
    });
  }

  async function handleTest(channelId: string, channelName: string) {
    try {
      const res = await testChannel.mutateAsync(channelId);
      const r = res.data;
      if (r.success) {
        toast.success(`${channelName}: OK (${r.latency_ms}ms, model: ${r.model})`);
      } else {
        toast.error(`${channelName}: ${r.error ?? "test failed"}${r.latency_ms ? ` (${r.latency_ms}ms)` : ""}`);
      }
    } catch {
      toast.error(`${channelName}: connection test failed`);
    }
  }

  function fmtNum(n: number): string {
    if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
    if (n >= 1_000) return `${(n / 1_000).toFixed(1)}K`;
    return String(n);
  }

  const columns: Column<Channel>[] = [
    {
      header: "ID",
      accessor: (c) => (
        <code className="text-xs text-muted-foreground">{c.id.slice(0, 8)}</code>
      ),
      className: "w-24",
    },
    { header: "Name", accessor: "name" },
    { header: "Provider", accessor: "provider" },
    {
      header: "Status",
      accessor: (c) => <StatusBadge status={c.status} />,
    },
    {
      header: "Models",
      accessor: (c) => c.supported_models?.join(", ") || "—",
    },
    {
      header: "Requests",
      accessor: (c) => {
        const s = statsMap.get(c.id);
        return s ? fmtNum(s.request_count) : "—";
      },
      className: "text-right",
    },
    {
      header: "Tokens",
      accessor: (c) => {
        const s = statsMap.get(c.id);
        return s ? fmtNum(s.input_tokens + s.output_tokens) : "—";
      },
      className: "text-right",
    },
    {
      header: "Avg Latency",
      accessor: (c) => {
        const s = statsMap.get(c.id);
        return s ? `${Math.round(s.avg_latency_ms)}ms` : "—";
      },
      className: "text-right",
    },
    {
      header: "Weight",
      accessor: (c) => String(c.weight),
      className: "text-right",
    },
    {
      header: "Priority",
      accessor: (c) => String(c.selection_priority),
      className: "text-right",
    },
    {
      header: "",
      accessor: (c) => (
        <DropdownMenu>
          <DropdownMenuTrigger
            render={<Button variant="ghost" size="icon" className="h-8 w-8" />}
          >
            <MoreHorizontal className="h-4 w-4" />
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuItem
              onClick={() => handleTest(c.id, c.name)}
              disabled={testChannel.isPending}
            >
              <Zap className="mr-2 h-4 w-4" />
              Test Connection
            </DropdownMenuItem>
            {c.status === "active" ? (
              <DropdownMenuItem
                onClick={() =>
                  updateChannel.mutate({ channelId: c.id, status: "disabled" })
                }
              >
                Disable
              </DropdownMenuItem>
            ) : (
              <DropdownMenuItem
                onClick={() =>
                  updateChannel.mutate({ channelId: c.id, status: "active" })
                }
              >
                Enable
              </DropdownMenuItem>
            )}
            <DropdownMenuItem
              className="text-destructive-foreground"
              onClick={() => deleteChannel.mutate(c.id)}
            >
              Delete
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      ),
      className: "w-12",
    },
  ];

  return (
    <div className="space-y-6">
      <PageHeader
        title="Channels"
        description="Manage upstream AI provider channels (superadmin only)"
        actions={
          <Button onClick={() => setShowCreate(true)}>
            <Plus className="mr-2 h-4 w-4" />
            Add Channel
          </Button>
        }
      />

      <Card>
        <CardContent className="p-0">
          {isLoading ? (
            <div className="flex items-center gap-2 p-6 text-muted-foreground">
              <Loader2 className="h-4 w-4 animate-spin" />
              Loading...
            </div>
          ) : (
            <DataTable
              columns={columns}
              data={channels}
              keyFn={(c) => c.id}
              emptyMessage="No channels configured"
            />
          )}
        </CardContent>
      </Card>

      <Dialog open={showCreate} onOpenChange={setShowCreate}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle>Add Channel</DialogTitle>
          </DialogHeader>
          <div className="space-y-4 py-4">
            <div className="space-y-2">
              <Label>Provider</Label>
              <Select value={form.provider} onValueChange={(v) => { if (v) updateForm("provider", v); }}>
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="anthropic">Anthropic</SelectItem>
                  <SelectItem value="openai">OpenAI</SelectItem>
                  <SelectItem value="gemini">Gemini</SelectItem>
                  <SelectItem value="bedrock">AWS Bedrock</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-2">
              <Label>Name</Label>
              <Input
                value={form.name}
                onChange={(e) => updateForm("name", e.target.value)}
                placeholder="anthropic-primary"
              />
            </div>
            <div className="space-y-2">
              <Label>Base URL</Label>
              <Input
                value={form.base_url}
                onChange={(e) => updateForm("base_url", e.target.value)}
                placeholder="https://api.anthropic.com"
              />
            </div>
            <div className="space-y-2">
              <Label>API Key</Label>
              <Input
                type="password"
                value={form.api_key}
                onChange={(e) => updateForm("api_key", e.target.value)}
                placeholder="sk-..."
              />
            </div>
            <div className="space-y-2">
              <Label>Supported Models (comma-separated)</Label>
              <Input
                value={form.supported_models}
                onChange={(e) => updateForm("supported_models", e.target.value)}
                placeholder="claude-opus-4, claude-sonnet-4"
              />
            </div>
            <div className="space-y-2">
              <Label>Test Model (optional, for connectivity test)</Label>
              <Input
                value={form.test_model}
                onChange={(e) => updateForm("test_model", e.target.value)}
                placeholder="claude-haiku-4-5"
              />
            </div>
            <div className="grid grid-cols-3 gap-4">
              <div className="space-y-2">
                <Label>Weight</Label>
                <Input
                  type="number"
                  value={form.weight}
                  onChange={(e) => updateForm("weight", e.target.value)}
                />
              </div>
              <div className="space-y-2">
                <Label>Priority</Label>
                <Input
                  type="number"
                  value={form.selection_priority}
                  onChange={(e) => updateForm("selection_priority", e.target.value)}
                />
              </div>
              <div className="space-y-2">
                <Label>Max Concurrent</Label>
                <Input
                  type="number"
                  value={form.max_concurrent}
                  onChange={(e) => updateForm("max_concurrent", e.target.value)}
                />
              </div>
            </div>
          </div>
          <DialogFooter>
            <Button
              onClick={handleCreate}
              disabled={!form.name || !form.base_url || !form.api_key || createChannel.isPending}
            >
              {createChannel.isPending ? "Creating..." : "Create Channel"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
