import { useCurrentProject } from "@/hooks/useCurrentProject";
import { useProjectModels, type ProjectModel } from "@/api/models";
import { PageHeader } from "@/components/layout/PageHeader";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Info } from "lucide-react";

function ModelCard({ model }: { model: ProjectModel }) {
  return (
    <Card>
      <CardHeader className="pb-2">
        <div className="flex items-start justify-between gap-2">
          <div className="min-w-0">
            <CardTitle className="text-base">{model.display_name || model.name}</CardTitle>
            <p className="mt-0.5 font-mono text-xs text-muted-foreground truncate">{model.name}</p>
          </div>
          {model.publisher && (
            <Badge variant="outline" className="shrink-0 text-xs">
              {model.publisher}
            </Badge>
          )}
        </div>
      </CardHeader>
      <CardContent className="space-y-3">
        {model.description && (
          <p className="text-sm text-muted-foreground">{model.description}</p>
        )}
        {model.metadata?.capabilities && model.metadata.capabilities.length > 0 && (
          <div className="flex flex-wrap gap-1">
            {model.metadata.capabilities.map((c) => (
              <Badge key={c} variant="secondary" className="text-[10px] capitalize">
                {c.replace(/_/g, " ")}
              </Badge>
            ))}
          </div>
        )}
        <div className="grid grid-cols-2 gap-x-3 gap-y-1 text-xs">
          {model.metadata?.context_window && (
            <>
              <span className="text-muted-foreground">Context</span>
              <span className="text-right tabular-nums">
                {model.metadata.context_window.toLocaleString()} tokens
              </span>
            </>
          )}
          {model.metadata?.category && (
            <>
              <span className="text-muted-foreground">Category</span>
              <span className="text-right capitalize">{model.metadata.category}</span>
            </>
          )}
        </div>
        {model.aliases.length > 0 && (
          <div className="border-t pt-2">
            <p className="mb-1 text-[10px] font-medium uppercase tracking-wider text-muted-foreground">
              Aliases (also accepted)
            </p>
            <div className="flex flex-wrap gap-1">
              {model.aliases.map((a) => (
                <code
                  key={a}
                  className="rounded bg-muted px-1.5 py-0.5 font-mono text-[11px]"
                >
                  {a}
                </code>
              ))}
            </div>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

function PricingInfo() {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <Info className="h-4 w-4" />
          模型定价说明
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-3 text-sm leading-relaxed text-muted-foreground">
        <p>平台目前通过以下方式确定模型单价：</p>
        <ul className="ml-4 list-disc space-y-1.5">
          <li>
            <span className="font-medium text-foreground">Anthropic 模型：</span>
            ETOChat Pro / Max 5x / Max 20x 对齐 Claude 官方 Pro / Max 5x / Max 20x 套餐配额，详情请参考{" "}
            <a
              href="https://claude.com/pricing"
              target="_blank"
              rel="noreferrer"
              className="text-primary hover:underline"
            >
              claude.com/pricing
            </a>
          </li>
          <li>
            <span className="font-medium text-foreground">OpenAI 模型：</span>
            ETOChat Pro / Max 5x / Max 20x 对齐 ChatGPT 官方 Plus / Pro 5x / Pro 20x 套餐配额，详情请参考{" "}
            <a
              href="https://chatgpt.com/pricing"
              target="_blank"
              rel="noreferrer"
              className="text-primary hover:underline"
            >
              chatgpt.com/pricing
            </a>
          </li>
          <li>
            <span className="font-medium text-foreground">其他国产模型：</span>
            ETOChat Max 2x 对齐阿里云百炼 Coding Plan，详情请参考{" "}
            <a
              href="https://help.aliyun.com/zh/model-studio/coding-plan"
              target="_blank"
              rel="noreferrer"
              className="text-primary hover:underline"
            >
              阿里云百炼文档
            </a>
          </li>
        </ul>
        <p>
          <span className="font-medium text-foreground">"对齐"</span> 指当用户全部使用某一类型的模型时，可用的配额上限与官方保持一致。由于上游平台不一定公开准确的配额限制，平台将通过线性回归等手段对官方配额进行合理推测，ETOChat 提供给用户的套餐配额也会相应进行动态调整。
        </p>
        <p>
          当 5h / 7d 配额已用尽后，若用户已开通 Extra Usage 且余额为正，后续请求会被放行；
          这些请求<span className="font-medium text-foreground">全额</span>按官方 API 价格从 Extra Usage 余额扣减
          （并非仅扣除超出部分），余额耗尽后请求将被限流拒绝。
        </p>
      </CardContent>
    </Card>
  );
}

export function ProjectModelsPage() {
  const projectId = useCurrentProject();
  const { data, isLoading } = useProjectModels(projectId);
  const models = data?.data ?? [];

  // Group by publisher for visual organization. Falls back to "Other" so
  // models without a publisher still render.
  const grouped = new Map<string, ProjectModel[]>();
  for (const m of models) {
    const key = m.publisher || "Other";
    const arr = grouped.get(key) ?? [];
    arr.push(m);
    grouped.set(key, arr);
  }
  const publishers = [...grouped.keys()].sort();

  return (
    <div className="space-y-6">
      <PageHeader
        title="Models"
        description="Models you can call from this project. Use the canonical name or any alias as the request's model field."
      />

      <PricingInfo />

      {isLoading ? (
        <p className="text-muted-foreground">Loading models...</p>
      ) : models.length === 0 ? (
        <p className="text-muted-foreground">No active models available.</p>
      ) : (
        publishers.map((pub) => (
          <section key={pub} className="space-y-3">
            <h2 className="text-sm font-semibold uppercase tracking-wider text-muted-foreground">
              {pub}
            </h2>
            <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-3">
              {grouped.get(pub)!.map((m) => (
                <ModelCard key={m.name} model={m} />
              ))}
            </div>
          </section>
        ))
      )}
    </div>
  );
}
