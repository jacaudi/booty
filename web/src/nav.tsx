import type { ReactNode } from 'react'
import HomeView from './views/HomeView'
import HostsView from './views/HostsView'
import OSImagesView from './views/OSImagesView'
import BootConfigsView from './views/BootConfigsView'
import ClustersView from './views/ClustersView'
import AboutView from './views/AboutView'

export interface NavEntry {
  path: string
  label: string
  element: ReactNode
}

// Single source of truth for routes AND the nav menu. A later slice adds one
// entry here plus its view file; the shell and sibling views are untouched.
//
// /images (was /cache) needs no Go change: pkg/http/ui.go:30-32 serves any
// extensionless path as the SPA shell.
export const navEntries: NavEntry[] = [
  { path: '/', label: 'Home', element: <HomeView /> },
  { path: '/hosts', label: 'Hosts', element: <HostsView /> },
  { path: '/images', label: 'OS Images', element: <OSImagesView /> },
  { path: '/boot-configs', label: 'Boot Configs', element: <BootConfigsView /> },
  { path: '/clusters', label: 'Clusters', element: <ClustersView /> },
  { path: '/about', label: 'About', element: <AboutView /> },
]
