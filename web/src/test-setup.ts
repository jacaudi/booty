import '@testing-library/jest-dom'
import { vi } from 'vitest'

Object.defineProperty(window, 'matchMedia', {
  writable: true,
  value: (query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addListener: vi.fn(),
    removeListener: vi.fn(),
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    dispatchEvent: vi.fn(),
  }),
})

// jsdom does not implement the pseudo-element argument of getComputedStyle and
// prints "Not implemented" on every call; AntD's rc-util getScrollBarSize passes
// one. Drop the argument (the answer jsdom gives is the same either way).
const realGetComputedStyle = window.getComputedStyle.bind(window)
window.getComputedStyle = (elt: Element) => realGetComputedStyle(elt)
