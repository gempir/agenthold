import { useQuery } from '@tanstack/react-query';

interface VersionInfo {
  version: string;
  commit: string;
}

async function fetchVersion(): Promise<VersionInfo> {
  const res = await fetch('/api/v1/version');
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  return (await res.json()) as VersionInfo;
}

export default function App() {
  const { data, isPending, isError } = useQuery({
    queryKey: ['version'],
    queryFn: fetchVersion,
    retry: false,
  });

  let status = 'connecting...';
  if (isError) status = 'server unreachable';
  else if (!isPending && data) status = `version ${data.version}`;

  return (
    <main className="mx-auto max-w-2xl p-8">
      <h1 className="text-3xl font-semibold">agenthold</h1>
      <p className="mt-2 text-sm text-gray-500">{status}</p>
    </main>
  );
}
