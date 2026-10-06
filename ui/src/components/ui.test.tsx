import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { expect, it } from 'vitest'
import '../lib/i18n'
import { Modal, Status } from './ui'
it('distinguishes recovery gaps from connectivity success', () => {
  render(
    <>
      <Status value="connected" />
      <Status value="gap_detected" />
    </>,
  )
  expect(screen.getByText('Connected')).toHaveClass('good')
  expect(screen.getByText('Recovery gap')).toHaveClass('bad')
})
it('renders dialog payload as inert text and supplies accessible labels', async () => {
  const user = userEvent.setup()
  const payload = '<img src=x onerror="alert(1)">'
  render(
    <Modal open onOpenChange={() => {}} title="Delivery details" description="Synthetic account">
      <pre>{payload}</pre>
      <button>Retry</button>
    </Modal>,
  )
  expect(screen.getByRole('dialog', { name: 'Delivery details' })).toBeInTheDocument()
  expect(screen.getByText(payload)).toBeInTheDocument()
  expect(document.querySelector('img')).toBeNull()
  await user.tab()
  expect(screen.getByRole('button', { name: 'Retry' })).toHaveFocus()
})
