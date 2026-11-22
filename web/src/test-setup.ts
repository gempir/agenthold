import * as matchers from '@testing-library/jest-dom/matchers';
import { cleanup } from '@testing-library/react';
import { afterEach, expect } from 'bun:test';

expect.extend(matchers);

declare module 'bun:test' {
  // Module augmentation adds the DOM matchers to Bun's existing interface.
  // eslint-disable-next-line @typescript-eslint/no-empty-object-type
  interface Matchers<T> extends Omit<
    matchers.TestingLibraryMatchers<
      ReturnType<typeof expect.stringContaining>,
      T
    >,
    'toBeEmpty'
  > {}
}

const globals = new Map<string, PropertyDescriptor | undefined>();

export function stubGlobal(name: string, value: unknown) {
  if (!globals.has(name))
    globals.set(name, Object.getOwnPropertyDescriptor(globalThis, name));
  Object.defineProperty(globalThis, name, {
    configurable: true,
    writable: true,
    value,
  });
}

afterEach(() => {
  cleanup();
  for (const [name, descriptor] of globals) {
    if (descriptor) Object.defineProperty(globalThis, name, descriptor);
    else Reflect.deleteProperty(globalThis, name);
  }
  globals.clear();
});
