import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import type { HttpLogDocument } from "@/api/types";

function HeadersTable({ headers }: { headers: Record<string, string[]> }) {
  const entries = Object.entries(headers);
  if (entries.length === 0) return <p className="text-muted-foreground text-xs">No headers</p>;
  return (
    <div className="rounded border text-xs">
      {entries.map(([key, values]) => (
        <div key={key} className="flex border-b last:border-b-0 px-2 py-1">
          <span className="font-medium text-muted-foreground w-[40%] shrink-0 break-all">{key}</span>
          <span className="font-mono break-all">{values.join(", ")}</span>
        </div>
      ))}
    </div>
  );
}

function JsonBody({ data }: { data: unknown }) {
  if (data === null || data === undefined) {
    return <p className="text-muted-foreground text-xs">No body</p>;
  }
  const text = typeof data === "string" ? data : JSON.stringify(data, null, 2);
  return (
    <pre className="whitespace-pre-wrap break-all rounded bg-muted p-3 text-xs max-h-[400px] overflow-y-auto">
      {text}
    </pre>
  );
}

export function HttpLogViewer({ data }: { data: HttpLogDocument }) {
  return (
    <div className="space-y-2">
      {data.truncated && (
        <p className="text-xs text-amber-600">Body was truncated due to size limits.</p>
      )}
      <Tabs defaultValue="request">
        <TabsList className="w-full">
          <TabsTrigger value="request" className="flex-1">Request</TabsTrigger>
          <TabsTrigger value="response" className="flex-1">
            Response ({data.response_status_code})
          </TabsTrigger>
        </TabsList>
        <TabsContent value="request" className="space-y-3 pt-2">
          <div className="space-y-1">
            <span className="text-xs font-medium text-muted-foreground">Headers</span>
            <HeadersTable headers={data.request_headers ?? {}} />
          </div>
          <div className="space-y-1">
            <span className="text-xs font-medium text-muted-foreground">Body</span>
            <JsonBody data={data.request_body} />
          </div>
        </TabsContent>
        <TabsContent value="response" className="space-y-3 pt-2">
          <div className="space-y-1">
            <span className="text-xs font-medium text-muted-foreground">Headers</span>
            <HeadersTable headers={data.response_headers ?? {}} />
          </div>
          <div className="space-y-1">
            <span className="text-xs font-medium text-muted-foreground">Body</span>
            <JsonBody data={data.response_body} />
          </div>
        </TabsContent>
      </Tabs>
    </div>
  );
}
