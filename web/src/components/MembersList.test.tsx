import { render, screen } from '@testing-library/react';
import { expect, it, vi } from 'vitest';
import type { Member } from '@/api/queries';
import { MembersList } from './MembersList';

const user = (id: string, name: string) => ({
  id,
  name,
  email: `${name}@x.io`,
  avatarUrl: '',
});
const members: Member[] = [
  { user: user('u1', 'Olga'), role: 'owner', joinedAt: '2030-01-01T00:00:00Z' },
  { user: user('u2', 'Mia'), role: 'member', joinedAt: '2030-01-01T00:00:00Z' },
];

const noop = vi.fn();

it('hides role controls for a plain member but allows leaving', () => {
  render(
    <MembersList
      members={members}
      myId="u2"
      myRole="member"
      personal={false}
      onChangeRole={noop}
      onRemove={noop}
      onLeave={noop}
    />,
  );
  expect(screen.queryByRole('combobox')).not.toBeInTheDocument();
  expect(
    screen.queryByRole('button', { name: 'Remove' }),
  ).not.toBeInTheDocument();
  expect(screen.getByRole('button', { name: 'Leave' })).toBeInTheDocument();
});

it('shows role controls for owners on other members, not on self', () => {
  render(
    <MembersList
      members={members}
      myId="u1"
      myRole="owner"
      personal={false}
      onChangeRole={noop}
      onRemove={noop}
      onLeave={noop}
    />,
  );
  expect(screen.getAllByRole('combobox')).toHaveLength(1);
  expect(
    screen.getByRole('combobox', { name: 'Role for Mia' }),
  ).toBeInTheDocument();
  expect(screen.getAllByRole('button', { name: 'Remove' })).toHaveLength(1);
});

it('does not offer Leave in a personal workspace', () => {
  render(
    <MembersList
      members={[members[0]]}
      myId="u1"
      myRole="owner"
      personal
      onChangeRole={noop}
      onRemove={noop}
      onLeave={noop}
    />,
  );
  expect(
    screen.queryByRole('button', { name: 'Leave' }),
  ).not.toBeInTheDocument();
});
