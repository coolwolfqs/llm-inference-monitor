// @vitest-environment jsdom
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { api } from '../src/services/api'

describe('model-manager API contract', () => {
  beforeEach(() => {
    sessionStorage.clear()
    vi.restoreAllMocks()
  })

  it('keeps catalog reads unauthenticated', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify({ models: [] }), { status: 200 }),
    )

    await api.models()

    expect(fetchMock).toHaveBeenCalledWith(
      '/model-manager/api/models',
      expect.objectContaining({ headers: expect.any(Headers) }),
    )
    const request = fetchMock.mock.calls[0][1]
    expect(new Headers(request?.headers).has('X-Admin-Key')).toBe(false)
  })

  it('adds the admin key to state-changing requests', async () => {
    vi.spyOn(window, 'prompt').mockReturnValue('test-key')
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify({ stopped: true }), { status: 200 }),
    )

    await api.stop()

    const request = fetchMock.mock.calls[0][1]
    expect(new Headers(request?.headers).get('X-Admin-Key')).toBe('test-key')
  })

  it('uses the cancellable download route', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify({ state: 'cancelled', phase: 'cancelling' }), { status: 200 }),
    )
    sessionStorage.setItem('dashboard_admin_key', 'test-key')

    await api.cancelDownload('dl/example')

    expect(fetchMock.mock.calls[0][0]).toBe('/model-manager/api/hub/downloads/dl%2Fexample/cancel')
    expect(fetchMock.mock.calls[0][1]?.method).toBe('POST')
  })
})
