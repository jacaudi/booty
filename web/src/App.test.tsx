import { describe, expect, it } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import App from './App'

describe('App shell', () => {
  it('renders the brand and nav links', () => {
    render(
      <MemoryRouter initialEntries={['/']}>
        <App />
      </MemoryRouter>,
    )
    // Both the header brand and HomeView render "Booty"/"Hosts" at "/", so scope
    // the nav-link assertions to the antd Menu (role="menu") to stay unambiguous.
    expect(screen.getAllByText('Booty').length).toBeGreaterThan(0)
    const menu = screen.getByRole('menu')
    expect(within(menu).getByRole('link', { name: 'Home' })).toBeInTheDocument()
    expect(within(menu).getByRole('link', { name: 'Hosts' })).toBeInTheDocument()
    expect(within(menu).getByRole('link', { name: 'About' })).toBeInTheDocument()
  })
})
