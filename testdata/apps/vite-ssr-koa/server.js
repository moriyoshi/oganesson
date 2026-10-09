// The Express template's server.js (create-vite-extra), on Koa.
import fs from 'node:fs/promises'
import Koa from 'koa'

const isProduction = process.env.NODE_ENV === 'production'
const port = process.env.PORT || 5173
const base = process.env.BASE || '/'

const templateHtml = isProduction ? await fs.readFile('./dist/client/index.html', 'utf-8') : ''

const app = new Koa()

/** @type {import('vite').ViteDevServer | undefined} */
let vite
if (!isProduction) {
  const { createServer } = await import('vite')
  vite = await createServer({ server: { middlewareMode: true }, appType: 'custom', base })
  app.use((ctx, next) => new Promise((resolve) => vite.middlewares(ctx.req, ctx.res, () => resolve(next()))))
} else {
  app.use((await import('koa-compress')).default())
  app.use((await import('koa-static')).default('./dist/client', { index: false }))
}

app.use(async (ctx) => {
  try {
    const url = ctx.originalUrl.replace(base, '')
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
    ctx.type = 'html'
    ctx.body = template.replace(`<!--app-head-->`, rendered.head ?? '').replace(`<!--app-html-->`, rendered.html ?? '')
  } catch (e) {
    vite?.ssrFixStacktrace(e)
    ctx.status = 500
    ctx.body = e.stack
  }
})

app.listen(port, () => {
  console.log(`Server started at http://localhost:${port}`)
})
