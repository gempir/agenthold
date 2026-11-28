import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, expect, it } from 'bun:test';
import { PushNotificationsSection } from './PushNotificationsSection';

afterEach(cleanup);
it('explains browser support and iOS installation requirements', () => {
  render(<PushNotificationsSection />);
  expect(screen.getByText('Push notifications')).toBeTruthy();
  expect(screen.getByText(/not supported in this browser/)).toBeTruthy();
  expect(screen.queryByText('Enable notifications')).toBeNull();
});
