import { useEffect } from 'react';
import { useNavigate } from '@tanstack/react-router';
import { Loading } from '@/components/Spinner';
import { TopBar } from '@/components/layout/TopBar';
import { useChatIndex } from '@/hooks/useChatIndex';

/** Landing: reopen the most recent conversation, or start a new one. */
export function HomePage() {
  const navigate = useNavigate();
  const { chats, loading, error } = useChatIndex();
  const latest = chats[0];
  useEffect(() => {
    if (loading && !error) return;
    if (latest)
      void navigate({
        to: '/a/$agentId',
        params: { agentId: latest.agent.id },
        search: { s: latest.session.id },
        replace: true,
      });
    else void navigate({ to: '/new', replace: true });
  }, [loading, error, latest, navigate]);
  return (
    <>
      <TopBar />
      <Loading />
    </>
  );
}
