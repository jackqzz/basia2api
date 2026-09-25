import { useQuery } from "@tanstack/react-query";
import { Card } from "@/components/ui/card";
import { Table, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import { PageHeader } from "@/components/shared/page-header";
import { PageSkeleton } from "@/components/shared/page-skeleton";
import { adminApi } from "@/lib/api";

const fmt = (n: number) =>
  n >= 1e6 ? (n / 1e6).toFixed(1) + "M" : n >= 1e3 ? (n / 1e3).toFixed(1) + "k" : String(n);

function Sparkline({ data }: { data: number[] }) {
  const W = 720;
  const H = 56;
  const max = Math.max(10, ...data);
  const pts = data
    .map((v, i) => `${(i / Math.max(1, data.length - 1)) * W},${H - 4 - (v / max) * (H - 8)}`)
    .join(" ");
  return (
    <div>
      <svg viewBox={`0 0 ${W} ${H}`} className="h-14 w-full" preserveAspectRatio="none">
        <polyline points={pts} fill="none" stroke="var(--color-ember, #b4552d)" strokeWidth="2" />
      </svg>
      <div className="mt-1 text-[12px] text-ash">RPM 趋势（3 分钟，每 10s 一点，峰值 {fmt(max)}）</div>
    </div>
  );
}

function Stat({ label, value }: { label: string; value: string | number }) {
  return (
    <div className="min-w-[110px]">
      <div className="text-[28px] font-semibold leading-none tracking-tight text-ink">{value}</div>
      <div className="mt-1.5 text-[12px] text-ash">{label}</div>
    </div>
  );
}

export function MetricsPage() {
  const q = useQuery({
    queryKey: ["metrics"],
    queryFn: adminApi.metrics,
    refetchInterval: 2000,
  });
  const d = q.data;
  const models = Object.entries(d?.by_model ?? {}).sort((a, b) => b[1].rpm - a[1].rpm);

  return (
    <div>
      <PageHeader title="实时容量" description="60s 窗口 · 每 2s 自动刷新" />
      {q.isLoading || !d ? (
        <PageSkeleton kind="cards" />
      ) : (
        <div className="space-y-4">
          <Card className="p-5">
            <div className="flex flex-wrap gap-x-10 gap-y-4">
              <Stat label="RPM 打进来" value={d.global.rpm} />
              <Stat label="TPM tokens" value={fmt(d.global.tpm)} />
              <Stat label="在飞" value={d.inflight} />
              <Stat label="4xx/5xx RPM" value={d.global.rejected_rpm} />
              <Stat label="重试放大" value={d.global.retry_per_req.toFixed(2)} />
            </div>
            <div className="mt-5">
              <Sparkline data={d.rpm_trend} />
            </div>
          </Card>
          <Card className="p-0">
            <div className="border-b border-border px-3 py-2 text-[13px] text-ash">按模型</div>
            <Table>
              <THead>
                <TR>
                  <TH>模型</TH>
                  <TH className="text-right">RPM</TH>
                  <TH className="text-right">成功</TH>
                  <TH className="text-right">失败</TH>
                  <TH className="text-right">prompt tpm</TH>
                  <TH className="text-right">completion tpm</TH>
                  <TH className="text-right">重试/req</TH>
                </TR>
              </THead>
              <TBody>
                {models.length === 0 ? (
                  <TR>
                    <TD colSpan={7} className="py-6 text-center text-ash">
                      窗口内没有流量
                    </TD>
                  </TR>
                ) : (
                  models.map(([m, s]) => (
                    <TR key={m}>
                      <TD className="font-mono text-[13px]">{m}</TD>
                      <TD className="text-right">{s.rpm}</TD>
                      <TD className="text-right">{s.served_rpm}</TD>
                      <TD className="text-right">{s.rejected_rpm}</TD>
                      <TD className="text-right">{fmt(s.prompt_tpm)}</TD>
                      <TD className="text-right">{fmt(s.completion_tpm)}</TD>
                      <TD className="text-right">{s.retry_per_req.toFixed(2)}</TD>
                    </TR>
                  ))
                )}
              </TBody>
            </Table>
          </Card>
        </div>
      )}
    </div>
  );
}
