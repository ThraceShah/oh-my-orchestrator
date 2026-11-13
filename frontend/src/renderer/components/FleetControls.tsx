import { useEffect, useState } from 'react';
import { Server, RefreshCw, Power, Monitor } from 'lucide-react';
import { getApiBaseUrl } from '../lib/api-client';
import { Button } from './ui/button';
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle, DialogTrigger } from './ui/dialog';
import { SidebarMenuButton, SidebarMenuItem } from './ui/sidebar';

type Node = { id: string; name: string; presence: string; availability: string; platforms?: string[]; capabilities?: { harness: string; loggedIn: boolean }[] };

/** Fleet actions share the desktop shell and dialog primitives. Enrollment stays
 * on the host CLI; a browser receives only the public node summary. */
export function FleetControls() {
  const [open, setOpen] = useState(false);
  const [nodes, setNodes] = useState<Node[]>([]);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState('');
  async function load() {
    try {
      const response = await fetch(`${getApiBaseUrl()}/fleet/nodes`);
      if (!response.ok) throw new Error('无法读取节点');
      setNodes((await response.json()).nodes || []);
      setError('');
    } catch (error) { setError(String(error instanceof Error ? error.message : error)); }
  }
  useEffect(() => {
    if (!open) return;
    void load();
    const timer = setInterval(() => void load(), 15000);
    return () => clearInterval(timer);
  }, [open]);
  async function power(id: string, action: 'wake' | 'shutdown') {
    if (action === 'shutdown' && !window.confirm('确认关闭这台按需机器？')) return;
    setBusy(id);
    try {
      const response = await fetch(`${getApiBaseUrl()}/fleet/nodes/${encodeURIComponent(id)}/power`, {
        method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ action }),
      });
      if (!response.ok) {
        const body = await response.json();
        throw new Error(body.error?.message || '电源操作失败');
      }
      await load();
    } catch (error) { setError(String(error instanceof Error ? error.message : error)); }
    finally { setBusy(''); }
  }
  return <SidebarMenuItem><Dialog open={open} onOpenChange={setOpen}>
    <DialogTrigger asChild><SidebarMenuButton tooltip="Fleet"><Server className="size-4" /><span>Fleet</span></SidebarMenuButton></DialogTrigger>
    <DialogContent className="max-w-2xl">
      <DialogHeader><DialogTitle>Fleet</DialogTitle><DialogDescription>任务由控制面调度到工作节点。</DialogDescription></DialogHeader>
      <div className="flex items-center justify-between text-xs text-muted-foreground"><span>{nodes.filter(n => n.presence === 'online').length} / {nodes.length} 在线</span><Button variant="ghost" size="sm" onClick={() => void load()}><RefreshCw className="size-3.5" />刷新</Button></div>
      <div className="divide-y divide-border">{nodes.map(node => <div key={node.id} className="flex items-start gap-3 py-3">
        <Monitor className="mt-0.5 size-4 text-muted-foreground" />
        <div className="min-w-0 flex-1"><div className="flex items-center gap-2 text-control font-medium">{node.name}<span className={`size-1.5 rounded-full ${node.presence === 'online' ? 'bg-status-ready' : 'bg-muted-foreground'}`} /><span className="text-xs font-normal text-muted-foreground">{node.presence}</span></div>
          <div className="mt-1 text-xs text-muted-foreground">{node.platforms?.join(' · ')} · {node.availability}</div>
          <div className="mt-1 text-xs text-muted-foreground">{node.capabilities?.filter(c => c.loggedIn).map(c => c.harness).join(' · ') || '尚无已登录的 Agent'}</div>
        </div>
        {node.availability === 'on-demand' && <div className="flex gap-1"><Button size="sm" variant="outline" disabled={busy === node.id} onClick={() => void power(node.id, 'wake')}><Power className="size-3.5" />唤醒</Button><Button size="sm" variant="ghost" disabled={busy === node.id} onClick={() => void power(node.id, 'shutdown')}>关机</Button></div>}
      </div>)}</div>
      {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
    </DialogContent>
  </Dialog></SidebarMenuItem>;
}
