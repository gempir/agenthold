import { describe, expect, it } from 'bun:test';
import { swipeAction, type SwipeGesture } from '@/components/layout/swipe';

const swipe = (over: Partial<SwipeGesture>): SwipeGesture => ({
  startX: 10,
  startY: 300,
  endX: 110,
  endY: 300,
  ...over,
});

describe('swipeAction', () => {
  it('opens the drawer on a rightward swipe from the left edge', () => {
    expect(swipeAction(swipe({}), false)).toBe('open');
  });

  it('ignores a rightward swipe that starts away from the edge', () => {
    expect(swipeAction(swipe({ startX: 200, endX: 300 }), false)).toBeNull();
  });

  it('closes the drawer on a leftward swipe anywhere', () => {
    expect(swipeAction(swipe({ startX: 250, endX: 150 }), true)).toBe('close');
  });

  it('does not reopen on a rightward swipe while open', () => {
    expect(swipeAction(swipe({}), true)).toBeNull();
  });

  it('does not close on a leftward swipe while closed', () => {
    expect(swipeAction(swipe({ startX: 250, endX: 150 }), false)).toBeNull();
  });

  it('ignores short swipes below the distance threshold', () => {
    expect(swipeAction(swipe({ endX: 40 }), false)).toBeNull();
  });

  it('ignores mostly vertical gestures so scrolling does not toggle', () => {
    expect(
      swipeAction(swipe({ startX: 10, endX: 70, endY: 500 }), false),
    ).toBeNull();
    expect(
      swipeAction(swipe({ startX: 250, endX: 190, endY: 600 }), true),
    ).toBeNull();
  });
});
