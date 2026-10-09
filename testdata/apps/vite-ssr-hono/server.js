// The Express template's server.js (create-vite-extra), on Hono with its
// Node adapter.
import fs from 'node:fs/promises'
import { Hono } from 'hono'
import { compress } from 'hono/compress'
import { serve } from '@hono/node-server'
import { serveStatic } from '@hono/node-server/serve-static'

const isProduction = process.env.NODE_ENV === 'production'
const port = Number(process.env.PORT || 5173)
const base = process.env.BASE || '/'

const templateHtml = isProduction ? await fs.readFile('./dist/client/index.html', 'utf-8') : ''

const app = new Hono()

/** @type {import('vite').ViteDevServer | undefined} */
let vite
if (!isProduction) {
  const { createServer } = await import('vite')
  vite = await createServer({ server: { middlewareMode: true }, appType: 'custom', base })
} else {
  app.use(compress())
  app.use('/assets/*', serveStatic({ root: './dist/client' }))
  app.use('/favicon.svg', serveStatic({ path: './dist/client/favicon.svg' }))
  app.use('/icons.svg', serveStatic({ path: './dist/client/icons.svg' }))
}

app.get('*', async (c) => {
  try {
    const url = c.req.path.replace(base, '')
    let template
    let render
    if (!isProduction) {
      template = await fs.readFile('./index.html', 'utf-8')
      template = await vite.transformIndexHtml(url, template)
      render = (await vite.ssrLoadModule('/src/entry-server.jsx')).render
    } else {
      template = templateHtml
      render = (await import('./dist/server/entry-server.js')).render
    }
    const rendered = await render(url)
    return c.html(template.replace(`<!--app-head-->`, rendered.head ?? '').replace(`<!--app-html-->`, rendered.html ?? ''))
  } catch (e) {
    vite?.ssrFixStacktrace(e)
    return c.text(e.stack, 500)
  }
})

serve({ fetch: app.fetch, port }, (info) => {
  console.log(`Server started at http://localhost:${info.port}`)
})
