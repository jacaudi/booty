import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import QuickActions from './QuickActions'

describe('QuickActions', () => {
  it('links to Hosts, OS Images, and Clusters', () => {
    render(<MemoryRouter><QuickActions /></MemoryRouter>)
    expect(screen.getByRole('link', { name: /approve hosts/i })).toHaveAttribute('href', '/hosts')
    expect(screen.getByRole('link', { name: /os images/i })).toHaveAttribute('href', '/images')
    expect(screen.getByRole('link', { name: /clusters/i })).toHaveAttribute('href', '/clusters')
  })
})
