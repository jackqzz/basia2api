import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useMemo, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { toast } from "sonner";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Table, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import { PageHeader } from "@/components/shared/page-header";
import { EmptyState } from "@/components/shared/empty-state";
import { PageSkeleton } from "@/components/shared/page-skeleton";
import { adminApi } from "@/lib/api";

function fmtExpiry(t: number) {
  if (!t) return "—";
  const d = new Date(t * 1000);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getMonth() + 1}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

export function PoolPage() {
  const qc = useQueryClient();
  const nav = useNavigate();
  const q = useQuery({
    queryKey: ["pool-accounts"],
    queryFn: adminApi.poolAccounts,
    refetchInterval: 30_000,
  });
  const [onlyDead, setOnlyDead] = useState(false);
  const [picked, setPicked] = useState<Set<string>>(new Set());

  const rows = useMemo(() => q.data?.accounts ?? [], [q.data]);
  const shown = useMemo(() => rows.filter((r) => !onlyDead || !r.ready), [rows, onlyDead]);
  const readyCount = rows.filter((r) => r.ready).length;

  const toggle = (name: string, on: boolean) => {
    setPicked((s) => {
      const n = new Set(s);
      if (on) n.add(name);
      else n.delete(name);
      return n;
    });
  };
  const allShownPicked = shown.length > 0 && shown.every((r) => picked.has(r.name));

  const act = useMutation({
    mutationFn: ({ action, names }: { action: string; names: string[] }) =>
      adminApi.accountActions(action, names),
    onSuccess: (d, v) => {
      toast.success(`已提交 ${v.names.length} 个账号（${v.action}）`, {
        action: d.task?.id
          ? { label: "查看任务", onClick: () => nav(`/tasks/${d.task.id}`) }
          : undefined,
      });
      setPicked(new Set());
      setTimeout(() => void qc.invalidateQueries({ queryKey: ["pool-accounts"] }), 3000);
      void qc.invalidateQueries({ queryKey: ["tasks"] });
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const run = (action: string) => {
    const names = shown.filter((r) => picked.has(r.name)).map((r) => r.name);
    if (!names.length) {
      toast.error("先勾选账号");
      return;
    }
    act.mutate({ action, names });
  };

  return (
    <div>
      <PageHeader
        title="账号池"
        description={`共 ${rows.length} 个账号 · 就绪 ${readyCount} · 未就绪 ${rows.length - readyCount}`}
        actions={
          <>
            <Button onClick={() => run("refresh_usage")} disabled={act.isPending}>
              测活选中
            </Button>
            <Button variant="outline" onClick={() => run("enable")} disabled={act.isPending}>
              启用选中
            </Button>
            <Button variant="outline" onClick={() => run("disable")} disabled={act.isPending}>
              禁用选中
            </Button>
            <Button variant="outline" onClick={() => void q.refetch()}>
              刷新
            </Button>
          </>
        }
      />
      {q.isLoading ? (
        <PageSkeleton kind="table" />
      ) : rows.length === 0 ? (
        <EmptyState
          title="池里还没有账号。先到账号页导入。"
          action={
            <Link to="/accounts">
              <Button>去账号页</Button>
            </Link>
          }
        />
      ) : (
        <Card className="p-0">
          <div className="flex items-center gap-2 border-b border-border px-3 py-2">
            <label className="flex cursor-pointer items-center gap-2 text-[13px] text-ash">
              <Checkbox checked={onlyDead} onCheckedChange={(v) => setOnlyDead(v === true)} />
              只看未就绪
            </label>
            <span className="text-[12px] text-ash">
              已选 {shown.filter((r) => picked.has(r.name)).length} / {shown.length}
            </span>
          </div>
          <Table>
            <THead>
              <TR>
                <TH className="w-8">
                  <Checkbox
                    checked={allShownPicked}
                    onCheckedChange={(v) =>
                      setPicked((s) => {
                        const n = new Set(s);
                        shown.forEach((r) => (v === true ? n.add(r.name) : n.delete(r.name)));
                        return n;
                      })
                    }
                  />
                </TH>
                <TH>账号</TH>
                <TH>邮箱</TH>
                <TH>账号ID</TH>
                <TH>套餐</TH>
                <TH>可续期</TH>
                <TH>状态</TH>
                <TH>连败</TH>
                <TH>token到期</TH>
                <TH>在飞</TH>
              </TR>
            </THead>
            <TBody>
              {shown.map((r) => (
                <TR key={r.name}>
                  <TD>
                    <Checkbox
                      checked={picked.has(r.name)}
                      onCheckedChange={(v) => toggle(r.name, v === true)}
                    />
                  </TD>
                  <TD>
                    <Link to={`/accounts/${encodeURIComponent(r.name)}`} className="text-ember hover:underline">
                      {r.name}
                    </Link>
                  </TD>
                  <TD className="text-[13px]">{r.email || "—"}</TD>
                  <TD className="font-mono text-[12px] text-ash">{r.account_id || "—"}</TD>
                  <TD>{r.plan || "—"}</TD>
                  <TD>{r.has_refresh ? "是" : "否"}</TD>
                  <TD>
                    <Badge tone={r.ready ? "ok" : "err"}>{r.ready ? "就绪" : "未就绪"}</Badge>
                    {!r.enabled && <Badge tone="muted" className="ml-1">已停用</Badge>}
                  </TD>
                  <TD>
                    {r.failures > 0 ? <Badge tone="err">{r.failures}</Badge> : <span className="text-ash">0</span>}
                  </TD>
                  <TD className="font-mono text-[12px]">{fmtExpiry(r.expires_at)}</TD>
                  <TD>{r.inflight}</TD>
                </TR>
              ))}
            </TBody>
          </Table>
        </Card>
      )}
    </div>
  );
}
