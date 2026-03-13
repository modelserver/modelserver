import { useState } from "react";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogFooter,
} from "@/components/ui/dialog";
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs";
import { Button } from "@/components/ui/button";
import { Copy, Check } from "lucide-react";

function CopyButton({ text }: { text: string }) {
  const [copied, setCopied] = useState(false);

  function handleCopy() {
    navigator.clipboard.writeText(text);
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  }

  return (
    <Button
      variant="ghost"
      size="icon-sm"
      className="absolute top-2 right-2 text-muted-foreground hover:text-foreground"
      onClick={handleCopy}
    >
      {copied ? <Check className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}
    </Button>
  );
}

function CodeBlock({ code }: { code: string }) {
  return (
    <div className="relative">
      <pre className="rounded-md bg-muted px-4 py-3 text-xs font-mono overflow-x-auto whitespace-pre-wrap break-all">
        {code}
      </pre>
      <CopyButton text={code} />
    </div>
  );
}

function Step({ n, title, children }: { n: number; title: string; children: React.ReactNode }) {
  return (
    <div className="space-y-2">
      <h4 className="text-sm font-medium">
        {n}. {title}
      </h4>
      {children}
    </div>
  );
}

export function UsageGuideDialog({
  open,
  onOpenChange,
  apiKey,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  apiKey: string;
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-2xl max-h-[85vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>Usage Guide</DialogTitle>
        </DialogHeader>
        <Tabs defaultValue="claude-code" className="py-2">
          <TabsList>
            <TabsTrigger value="claude-code">Claude Code</TabsTrigger>
            <TabsTrigger value="opencode">OpenCode</TabsTrigger>
          </TabsList>

          <TabsContent value="claude-code" className="space-y-4 pt-4">
            <Step n={1} title="Install Claude Code">
              <CodeBlock code="npm install -g @anthropic-ai/claude-code" />
            </Step>

            <Step n={2} title="Set environment variables">
              <CodeBlock
                code={`export ANTHROPIC_BASE_URL=https://code.ai.cs.ac.cn\nexport ANTHROPIC_API_KEY=${apiKey}`}
              />
            </Step>

            <Step n={3} title="Skip login">
              <p className="text-xs text-muted-foreground">
                Edit <code className="rounded bg-muted px-1 py-0.5">~/.claude.json</code> and add:
              </p>
              <CodeBlock code={`{\n  "hasCompletedOnboarding": true\n}`} />
            </Step>

            <Step n={4} title="Run">
              <CodeBlock code="claude" />
            </Step>
          </TabsContent>

          <TabsContent value="opencode" className="space-y-4 pt-4">
            <Step n={1} title="Install OpenCode">
              <CodeBlock code="npm install -g opencode-ai" />
            </Step>

            <Step n={2} title="Configure">
              <p className="text-xs text-muted-foreground">
                Edit{" "}
                <code className="rounded bg-muted px-1 py-0.5">~/.opencode/config.json</code> and
                add:
              </p>
              <CodeBlock
                code={`{\n  "provider": {\n    "anthropic": {\n      "apiKey": "${apiKey}",\n      "baseURL": "https://code.ai.cs.ac.cn"\n    }\n  }\n}`}
              />
            </Step>

            <Step n={3} title="Run">
              <CodeBlock code="cd your-project && opencode" />
            </Step>
          </TabsContent>
        </Tabs>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            Close
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
