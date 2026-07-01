import type { ReactNode } from 'react'
import HomeView from './views/HomeView'
import HostsView from './views/HostsView'
import AboutView from './views/AboutView'

export interface NavEntry {
  path: string
  label: string
  element: ReactNode
}

// Single source of truth for routes AND the nav menu. A later slice adds one
// entry here plus its view file; the shell and sibling views are untouched.
export const navEntries: NavEntry[] = [
  { path: '/', label: 'Home', element: <HomeView /> },
  { path: '/hosts', label: 'Hosts', element: <HostsView /> },
  { path: '/about', label: 'About', element: <AboutView /> },
]
