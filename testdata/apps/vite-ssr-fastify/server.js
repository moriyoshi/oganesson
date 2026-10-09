// The Express template's server.js (create-vite-extra), on Fastify.
import fs from 'node:fs/promises'
import path from 'node:path'
import Fastify from 'fastify'

const isProduction = process.env.NODE_ENV === 'production'
const port = process.env.PORT || 5173
const base = process.env.BASE || '/'

const templateHtml = isProduction ? await fs.readFile('./dist/client/index.html', 'utf-8') : ''

const app = Fastify()

/** @type {import('vite').ViteDevServer | undefined} */
let vite
if (!isProduction) {
  await app.register((await import('@fastify/middie')).default)
  const { createServer } = await import('vite')
  vite = await createServer({ server: { middlewareMode: true }, appType: 'custom', base })
  app.use(vite.middlewares)
} else {
  await app.register((await import('@fastify/compress')).default)
  await app.register((await import('@fastify/static')).default, {
    root: path.resolve('./dist/client'),
    prefix: base,
    index: false,
    wildcard: false,
  })
}

app.get('*', async (req, reply) => {
  try {
    const url = req.url.replace(base, '')
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
    const html = template.replace(`<!--app-head-->`, rendered.head ?? '').replace(`<!--app-html-->`, rendered.html ?? '')
    reply.code(200).type('text/html').send(html)
  } catch (e) {
    vite?.ssrFixStacktrace(e)
    reply.code(500).send(e.stack)
  }
})

await app.listen({ port })
console.log(`Server started at http://localhost:${port}`)
