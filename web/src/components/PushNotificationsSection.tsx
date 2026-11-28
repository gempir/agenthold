import { useEffect, useState } from 'react';
import { Button } from '@/components/ui/button';
import { Card } from '@/components/ui/card';

type Device = { id: string; endpoint: string; userAgent: string };
async function request<T>(
  path: string,
  method = 'GET',
  body?: unknown,
): Promise<T> {
  const response = await fetch(`/api/v1/me/push/${path}`, {
    method,
    headers: { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!response.ok) throw new Error('Notification request failed');
  return response.json();
}
export function PushNotificationsSection() {
  const supported =
    typeof Notification !== 'undefined' &&
    'serviceWorker' in navigator &&
    'PushManager' in window;
  const [devices, setDevices] = useState<Device[]>([]);
  const [current, setCurrent] = useState<PushSubscription | null>(null);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (!supported) return;
    void request<Device[]>('subscriptions')
      .then(setDevices)
      .catch(() => setError('Could not load devices'));
    void navigator.serviceWorker
      .getRegistration('/')
      .then(async (r) =>
        setCurrent((await r?.pushManager.getSubscription()) ?? null),
      );
  }, [supported]);
  const run = async (action: () => Promise<void>) => {
    setBusy(true);
    setError('');
    try {
      await action();
      setDevices(await request<Device[]>('subscriptions'));
    } catch (e) {
      setError(
        e instanceof Error ? e.message : 'Could not update notifications',
      );
    } finally {
      setBusy(false);
    }
  };
  const revoke = async (device: Device) => {
    await request(`subscriptions/${device.id}`, 'DELETE');
    if (device.endpoint === current?.endpoint) {
      await current.unsubscribe();
      setCurrent(null);
    }
  };
  const enable = async () => {
    const key = await request<{ enabled: boolean; publicKey: string }>(
      'vapid-key',
    );
    if (!key.enabled)
      throw new Error('Notifications are unavailable on this server');
    if ((await Notification.requestPermission()) !== 'granted')
      throw new Error('Allow notifications in your browser settings');
    const registration = await navigator.serviceWorker.register('/sw.js');
    await navigator.serviceWorker.ready;
    const raw = atob(key.publicKey.replace(/-/g, '+').replace(/_/g, '/'));
    const subscription = await registration.pushManager.subscribe({
      userVisibleOnly: true,
      applicationServerKey: Uint8Array.from(raw, (c) => c.charCodeAt(0)),
    });
    const json = subscription.toJSON();
    await request('subscriptions', 'POST', {
      endpoint: subscription.endpoint,
      p256dh: json.keys?.p256dh,
      auth: json.keys?.auth,
      userAgent: navigator.userAgent,
    });
    setCurrent(subscription);
  };
  return (
    <Card className="grid gap-3 p-4">
      <p className="text-sm font-medium">Push notifications</p>
      <p className="text-sm text-muted-foreground">
        {supported
          ? 'Get alerts when a turn finishes or needs your attention.'
          : 'Push notifications are not supported in this browser. On iOS, install agenthold on your Home Screen first.'}
      </p>
      {error && <p role="alert">{error}</p>}
      {supported && (
        <div className="flex gap-2">
          <Button
            disabled={busy}
            onClick={() =>
              void run(async () => {
                if (!current) return enable();
                for (const d of devices.filter(
                  (d) => d.endpoint === current.endpoint,
                ))
                  await revoke(d);
                await current.unsubscribe();
                setCurrent(null);
              })
            }
          >
            {current ? 'Disable on this device' : 'Enable notifications'}
          </Button>
          <Button
            variant="outline"
            disabled={busy || devices.length === 0}
            onClick={() =>
              void run(async () => {
                await request('test', 'POST');
              })
            }
          >
            Send test notification
          </Button>
        </div>
      )}
      {devices.map((d) => (
        <div className="flex items-center justify-between gap-2" key={d.id}>
          <span className="truncate text-sm">
            {d.userAgent || 'Browser device'}
          </span>
          <Button
            variant="outline"
            disabled={busy}
            onClick={() => void run(() => revoke(d))}
          >
            Revoke
          </Button>
        </div>
      ))}
    </Card>
  );
}
