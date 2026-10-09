// The Express template's server.js (create-vite-extra), on h3 v2 (whose
// serve() is srvx's Node adapter).
import fs from 'node:fs/promises'
import { stat } from 'node:fs/promises'
import { join } from 'node:path'
import { H3, serve, serveStatic, html, raw } from 'h3'

const isProduction = process.env.NODE_ENV === 'production'
const port = Number(process.env.PORT || 5173)
const base = process.env.BASE || '/'

const templateHtml = isProduction ? await fs.readFile('./dist/client/index.html', 'utf-8') : ''

const app = new H3()

/** @type {import('vite').ViteDevServer | undefined} */
let vite
if (!isProduction) {
  const { createServer } = await import('vite')
  vite = await createServer({ server: { middlewareMode: true }, appType: 'custom', base })
} else {
  app.use('/assets/**', (event) =>
    serveStatic(event, {
      getContents: (id) => fs.readFile(join('./dist/client', id)),
      getMeta: async (id) => {
        const s = await stat(join('./dist/client', id)).catch(() => {})
        if (s?.isFile()) return { size: s.size, mtime: s.mtimeMs }
      },
    }),
  )
}

app.get('/**', async (event) => {
  try {
    const url = event.url.pathname.replace(base, '')
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
    // h3 v2's html() escapes plain strings; the rendered page is trusted markup.
    return html(raw(template.replace(`<!--app-head-->`, rendered.head ?? '').replace(`<!--app-html-->`, rendered.html ?? '')))
  } catch (e) {
    vite?.ssrFixStacktrace(e)
    return new Response(e.stack, { status: 500 })
  }
})

serve(app, { port })
