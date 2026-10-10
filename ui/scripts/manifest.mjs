import { createHash } from 'node:crypto'
import { readFile, readdir, writeFile } from 'node:fs/promises'
import { resolve, relative } from 'node:path'
const root = resolve(import.meta.dirname, '../..')
const dist = resolve(root, 'src/ui/web/dist')
const hash = (data) => createHash('sha256').update(data).digest('hex')
const settings = await readFile(resolve(root, 'src/config/settings.go'), 'utf8')
const version = settings.match(/const AppVersion = "([^"]+)"/)?.[1]
if (!version) throw new Error('GoOmni AppVersion was not found')
// Ship notices for every runtime dependency, including fonts and transitives.
// Read installed lockfile content only: no network request during packaging.
const lock = JSON.parse(await readFile(resolve(root, 'ui/package-lock.json'), 'utf8'))
const notices = [
  'GoOmni admin UI — third-party notices',
  'GoOmni is distributed under LICENCE.txt. The dependencies below retain their own licenses.',
]
for (const [directory, metadata] of Object.entries(lock.packages).sort(([a], [b]) =>
  a.localeCompare(b),
)) {
  if (!directory || metadata.dev || metadata.devOptional) continue
  const folder = resolve(root, 'ui', directory)
  const pkg = JSON.parse(await readFile(resolve(folder, 'package.json'), 'utf8'))
  const names = (await readdir(folder)).filter((name) =>
    /^(licen[sc]e|notice|copying|ofl)([.-].*)?$/i.test(name),
  )
  const fallback =
    pkg.name === 'react-remove-scroll-bar' && pkg.version === '2.3.8'
      ? await readFile(resolve(root, 'ui/licenses/react-remove-scroll-bar-2.3.8-LICENSE'), 'utf8')
      : ''
  if (!pkg.license || (!names.length && !fallback))
    throw new Error(`Missing license evidence for ${pkg.name}`)
  notices.push(`\n${'='.repeat(72)}\n${pkg.name}@${pkg.version} — ${pkg.license}\n`)
  if (fallback)
    notices.push(
      `LICENSE (upstream LICENSE revision 8ca9ba5ea52de03308fe8ced94f7b159a44d28ff):\n${fallback}`,
    )
  for (const name of names)
    notices.push(`${name}:\n${await readFile(resolve(folder, name), 'utf8')}`)
}
await writeFile(resolve(dist, 'THIRD_PARTY_NOTICES.txt'), notices.join('\n') + '\n')
const files = {}
async function collect(directory) {
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    if (entry.name.startsWith('.') || entry.name === 'build-manifest.json') continue
    const path = resolve(directory, entry.name)
    if (entry.isDirectory()) await collect(path)
    else if (entry.isFile())
      files[relative(dist, path).replaceAll('\\', '/')] = hash(await readFile(path))
    else throw new Error('Build output must contain only regular files and directories')
  }
}
await collect(dist)
if (!files['index.html'] || Object.keys(files).length < 3)
  throw new Error('UI build output is incomplete')
await writeFile(
  resolve(dist, 'build-manifest.json'),
  JSON.stringify(
    {
      schema_version: 1,
      version,
      openapi_sha256: hash(await readFile(resolve(root, 'docs/openapi.yaml'))),
      files,
    },
    null,
    2,
  ) + '\n',
)
await writeFile(
  resolve(dist, '.placeholder'),
  'Generated frontend assets are ignored. Run npm ci && npm run build in ui/.\n',
)
console.log(`Embedded UI: ${version}, ${Object.keys(files).length} verified files`)
