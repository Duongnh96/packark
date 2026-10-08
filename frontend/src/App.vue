<script setup>
import { ref, computed, watch, onMounted } from 'vue'

const distros = ref([])
const distroId = ref('')
const version = ref('')
const packages = ref('')
const job = ref(null)
const error = ref('')
const logBox = ref(null)

const term = ref('')
const results = ref(null)
const searching = ref(false)
const searchError = ref('')
const searchLog = ref('')
const searchBox = ref(null)

const bundles = ref([])
const synced = ref('')

// Filter on everything a row shows: distro, release, packages, repositories, key.
const bundleQuery = ref('')
const bundlePage = ref(0)
const perPage = 10
const shownBundles = computed(() => {
  const words = bundleQuery.value.toLowerCase().split(/\s+/).filter(Boolean)
  return bundles.value.filter((b) => {
    const text = [b.distro, b.version, b.key, ...b.packages, ...(b.repos ?? [])]
      .join(' ')
      .toLowerCase()
    return words.every((w) => text.includes(w))
  })
})
const pages = computed(() => Math.max(1, Math.ceil(shownBundles.value.length / perPage)))
const pageBundles = computed(() =>
  shownBundles.value.slice(bundlePage.value * perPage, (bundlePage.value + 1) * perPage),
)
watch(bundleQuery, () => (bundlePage.value = 0))
// A delete can empty the last page; step back instead of showing nothing.
watch(pages, (n) => (bundlePage.value = Math.min(bundlePage.value, n - 1)))

// auto follows the operating system; the other two pin it. Kept per browser,
// which is the only place a preference like this belongs.
const themes = ['auto', 'dark', 'light']
const theme = ref(localStorage.getItem('packark-theme') || 'auto')
watch(
  theme,
  (t) => {
    if (t === 'auto') document.documentElement.removeAttribute('data-theme')
    else document.documentElement.setAttribute('data-theme', t)
    try {
      localStorage.setItem('packark-theme', t)
    } catch {
      // Private browsing refuses to store; the theme still applies for now.
    }
  },
  { immediate: true },
)
function cycleTheme() {
  theme.value = themes[(themes.indexOf(theme.value) + 1) % themes.length]
}
const copied = ref('')
const repos = ref([])

const distro = computed(() => distros.value.find((d) => d.id === distroId.value))
const release = computed(() => distro.value?.releases.find((r) => r.version === version.value))
const running = computed(() => job.value?.status === 'running')
const parsed = computed(() => packages.value.split(/\s+/).filter(Boolean))
const hint = computed(() =>
  distroId.value === 'oraclelinux' ? 'httpd podman python3' : 'nginx postgresql-16 curl',
)

// Releases carry their own optgroup label; keep the order the backend sent.
const groups = computed(() => {
  const out = []
  for (const r of distro.value?.releases ?? []) {
    const last = out[out.length - 1]
    if (last?.label === r.group) last.releases.push(r)
    else out.push({ label: r.group, releases: [r] })
  }
  return out
})

const install = computed(() => {
  const target = `${distroId.value || 'ubuntu'}-${version.value || '24.04'}`
  // Before a build there is no key yet, so the glob is what actually pastes.
  const name = `packark-${target}-${job.value?.key ?? '*'}.tar.gz`
  return `sha256sum ${name}\nmkdir bundle && tar xzf ${name} -C bundle\ncd bundle && sudo ./install.sh`
})

onMounted(async () => {
  const res = await fetch('/api/distros')
  // Empty until the first refresh lands, which means the built-in list.
  synced.value = res.headers.get('X-Releases-Synced') ?? ''
  distros.value = await res.json()
  distroId.value = distros.value[0]?.id ?? ''
  loadBundles()
})

// Versions belong to a distro, so reset the pick whenever the distro changes.
watch(distro, (d) => {
  version.value = d?.releases[0]?.version ?? ''
})

// Hits are per release; a different release means a different repository.
watch(version, () => {
  results.value = null
  searchError.value = ''
})

// Apt needs a whole sources.list line and a key; dnf takes the .repo file
// vendors publish. Switching distribution invalidates whatever was typed.
const apt = computed(() => distro.value?.repo_family === 'apt')
const repoExample = computed(() => distro.value?.presets?.[0]?.url ?? '')
const keyExample = computed(() => distro.value?.presets?.[0]?.key ?? '')
const sentRepos = computed(() =>
  repos.value
    .filter((r) => r.url.trim())
    .map((r) => ({ url: r.url.trim(), key: r.key.trim() || undefined })),
)
watch(distro, () => (repos.value = []))

// Results were produced against one repository set; a change invalidates them.
watch(sentRepos, () => (results.value = null), { deep: true })

// What a miss usually means on the distribution in front of you.
const missHint = computed(() =>
  apt.value
    ? `Only that release’s own pockets are searched, and ${distro.value?.name}’s Docker package is docker.io, not docker.`
    : 'Only the repositories that release enables are searched, and Oracle Linux 8 and 9 ship podman rather than docker.',
)

// A one-line entry stays one line; a pasted stanza grows the box to fit.
function lines(text) {
  return Math.min(14, Math.max(1, (text || '').split('\n').length))
}

function addRepo() {
  repos.value.push({ url: '', key: '' })
}

// Presets are the vendor's own lines, shipped with the distribution they belong
// to — Proxmox's wiki page is four repositories and a codename-specific key.
const presetChoice = ref('')
function addPreset() {
  const p = distro.value?.presets?.find((x) => x.name === presetChoice.value)
  presetChoice.value = ''
  if (p) repos.value.push({ url: p.url, key: p.key ?? '' })
}

async function loadBundles() {
  bundles.value = await (await fetch('/api/bundles')).json()
}

async function start() {
  error.value = ''
  const res = await fetch('/api/jobs', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      distro: distroId.value,
      version: version.value,
      packages: parsed.value,
      repos: sentRepos.value,
    }),
  })
  if (!res.ok) {
    error.value = (await res.text()).trim()
    return
  }
  const { id } = await res.json()
  job.value = { id, status: 'running', log: '' }
  poll(id)
}

async function poll(id) {
  job.value = await (await fetch(`/api/jobs/${id}`)).json()
  requestAnimationFrame(() => {
    if (logBox.value) logBox.value.scrollTop = logBox.value.scrollHeight
  })
  if (job.value.status === 'running') setTimeout(() => poll(id), 1000)
  else loadBundles()
}

// A search runs the release's own package manager in its own container, so it
// costs seconds at best and minutes on a cold image. It is a job like a build:
// start it, then stream what the container is doing until it finishes.
async function search() {
  const q = term.value.trim()
  if (!q) return
  searching.value = true
  searchError.value = ''
  searchLog.value = ''
  results.value = null
  try {
    const res = await fetch('/api/search', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        distro: distroId.value,
        version: version.value,
        q,
        repos: sentRepos.value,
      }),
    })
    if (!res.ok) throw new Error((await res.text()).trim())
    const { id } = await res.json()
    await pollSearch(id)
  } catch (e) {
    searchError.value = e.message
    searching.value = false
  }
}

async function pollSearch(id) {
  const s = await (await fetch(`/api/jobs/${id}`)).json()
  searchLog.value = s.log ?? ''
  requestAnimationFrame(() => {
    if (searchBox.value) searchBox.value.scrollTop = searchBox.value.scrollHeight
  })
  if (s.status === 'running') {
    setTimeout(() => pollSearch(id), 700)
    return
  }
  searching.value = false
  if (s.status === 'error') searchError.value = s.error || 'search failed'
  else results.value = s.hits ?? []
}

function toggle(name) {
  const list = parsed.value
  packages.value = list.includes(name)
    ? list.filter((p) => p !== name).join(' ')
    : [...list, name].join(' ')
}

// A pinned version is how the package manager spells it on the command line:
// apt takes name=version, dnf and yum take name-[epoch:]version-release.
function pin(name, v) {
  return apt.value ? `${name}=${v}` : `${name}-${v}`
}

// Which spelling of this hit is selected: the bare name, a pinned version, or none.
function picked(p) {
  const specs = [p.name, ...(p.versions ?? []).map((v) => pin(p.name, v))]
  return parsed.value.find((s) => specs.includes(s))
}

// Replace whatever spelling of p is selected with spec; null deselects it.
function pick(p, spec) {
  const old = picked(p)
  const list = parsed.value.filter((s) => s !== old)
  packages.value = (spec ? [...list, spec] : list).join(' ')
}

async function forget(key) {
  await fetch(`/api/bundles/${key}`, { method: 'DELETE' })
  if (job.value?.key === key) job.value = null
  loadBundles()
}

async function copy(what, text) {
  await navigator.clipboard.writeText(text)
  copied.value = what
  setTimeout(() => (copied.value = ''), 1500)
}

function size(n) {
  if (!n) return '—'
  const units = ['B', 'KB', 'MB', 'GB']
  let i = 0
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024
    i++
  }
  return `${n < 10 && i > 0 ? n.toFixed(1) : Math.round(n)} ${units[i]}`
}

function when(iso) {
  const d = new Date(iso)
  return `${d.toLocaleDateString(undefined, { day: '2-digit', month: 'short' })} ${d
    .toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', hour12: false })}`
}
</script>

<template>
  <div class="shell">
    <header class="topbar">
      <div class="brand">
        <span class="mark">packark</span><span class="caret"></span>
      </div>
      <p class="tagline">Offline package bundles for airgapped Linux machines</p>
      <button class="btn ghost theme" @click="cycleTheme" :title="`Theme: ${theme}`">
        {{ theme === 'auto' ? '◐' : theme === 'dark' ? '●' : '○' }} {{ theme }}
      </button>
    </header>

    <div class="grid">
      <!-- ── build column ─────────────────────────────────────────── -->
      <div class="col">
        <section class="panel" style="--i: 0">
          <h2 class="panel-title">01 · Target</h2>
          <div class="panel-body">
            <div class="two">
              <div class="field">
                <label for="distro">Distribution</label>
                <select id="distro" v-model="distroId" :disabled="running">
                  <option v-for="d in distros" :key="d.id" :value="d.id">{{ d.name }}</option>
                </select>
              </div>
              <div class="field">
                <label for="version">Release</label>
                <select id="version" v-model="version" :disabled="running">
                  <optgroup v-for="g in groups" :key="g.label" :label="g.label">
                    <option v-for="r in g.releases" :key="r.version" :value="r.version">
                      {{ r.name }}
                    </option>
                  </optgroup>
                </select>
              </div>
            </div>
            <p v-if="distro?.note" class="note">{{ distro.note }}</p>
          </div>
        </section>

        <section class="panel" style="--i: 1">
          <h2 class="panel-title">
            02 · Extra repositories
            <span v-if="sentRepos.length" class="state cache">{{ sentRepos.length }}</span>
          </h2>
          <div class="panel-body">
            <p class="note">
              Stock images carry only the distribution's own repositories. Add a vendor one to
              search and bundle what it serves: Proxmox VE, Docker CE, an internal mirror. Pick a
              known one below and the line and its key are filled in for you.
            </p>
            <div v-if="repos.length" class="repolist">
              <div v-for="(r, i) in repos" :key="i" class="repo" :class="{ tall: lines(r.url) > 1 }">
                <!-- A textarea, because a pasted .sources stanza is several lines. -->
                <textarea
                  v-model="r.url"
                  :disabled="running"
                  :placeholder="repoExample"
                  :rows="lines(r.url)"
                  spellcheck="false"
                  :aria-label="`Repository ${i + 1}`"
                ></textarea>
                <input
                  v-if="apt"
                  v-model="r.key"
                  :disabled="running"
                  :placeholder="keyExample"
                  spellcheck="false"
                  autocomplete="off"
                  :aria-label="`GPG key for repository ${i + 1}`"
                />
                <button
                  class="btn ghost"
                  :disabled="running"
                  :aria-label="`Remove repository ${i + 1}`"
                  @click="repos.splice(i, 1)"
                >
                  ×
                </button>
              </div>
            </div>
            <div class="actions">
              <button class="btn" :disabled="running" @click="addRepo">+ repository</button>
              <select
                v-if="distro?.presets?.length"
                class="preset"
                v-model="presetChoice"
                :disabled="running"
                aria-label="Add a known repository"
                @change="addPreset"
              >
                <option value="">add a known repository…</option>
                <option v-for="p in distro.presets" :key="p.name" :value="p.name" :title="p.note">
                  {{ p.name }}
                </option>
              </select>
              <span class="hintline">
                {{
                  apt
                    ? 'a sources.list line or a pasted .sources stanza, plus the key URL — $CODENAME is filled in by the container'
                    : 'the .repo file URL the vendor publishes'
                }}
              </span>
            </div>
          </div>
        </section>

        <section class="panel" style="--i: 2">
          <h2 class="panel-title">03 · Packages</h2>
          <div class="panel-body">
            <form class="searchbar" @submit.prevent="search">
              <input
                v-model="term"
                :disabled="running || searching"
                placeholder="search the release's own repositories…"
                spellcheck="false"
                autocomplete="off"
                aria-label="Search packages"
              />
              <button type="submit" class="btn" :disabled="running || searching || !term.trim()">
                {{ searching ? 'searching' : 'search' }}<span v-if="searching" class="blink"></span>
              </button>
            </form>

            <pre v-if="searching || (searchLog && searchError)" ref="searchBox" class="log mini">{{
              searchLog || 'starting…'
            }}</pre>
            <p v-if="searchError" class="banner err">{{ searchError }}</p>
            <div v-else-if="results?.length === 0" class="banner flat">
              <b>No match in {{ distro?.name }} {{ version }}.</b>
              {{ missHint }} A vendor package — Docker CE and friends — needs its repository
              added above before it can be found.
            </div>
            <ul v-else-if="results?.length" class="results">
              <li v-for="p in results" :key="p.name">
                <label>
                  <input
                    type="checkbox"
                    :checked="!!picked(p)"
                    :disabled="running"
                    @change="pick(p, picked(p) ? null : p.name)"
                  />
                  <span class="pkg">{{ p.name }}</span>
                  <span class="summary">{{ p.summary }}</span>
                </label>
                <details v-if="p.versions?.length" class="versions">
                  <summary>
                    {{ p.versions.length }} version{{ p.versions.length > 1 ? 's' : '' }}
                    <span v-if="picked(p) && picked(p) !== p.name" class="pinned">
                      pinned {{ picked(p).slice(p.name.length + 1) }}
                    </span>
                  </summary>
                  <label>
                    <input
                      type="radio"
                      :name="`v-${p.name}`"
                      :checked="picked(p) === p.name"
                      :disabled="running"
                      @change="pick(p, p.name)"
                    />
                    <span>latest</span>
                  </label>
                  <label v-for="v in p.versions" :key="v">
                    <input
                      type="radio"
                      :name="`v-${p.name}`"
                      :checked="picked(p) === pin(p.name, v)"
                      :disabled="running"
                      @change="pick(p, pin(p.name, v))"
                    />
                    <code>{{ v }}</code>
                  </label>
                </details>
              </li>
            </ul>

            <div class="field">
              <label for="packages">
                Selected
                <span class="count" :class="{ zero: !parsed.length }">{{ parsed.length }}</span>
              </label>
              <div v-if="parsed.length" class="chips">
                <button
                  v-for="p in parsed"
                  :key="p"
                  class="chip"
                  :disabled="running"
                  @click="toggle(p)"
                  :title="`Remove ${p}`"
                >
                  {{ p }}<span class="x">×</span>
                </button>
              </div>
              <textarea
                id="packages"
                v-model="packages"
                :disabled="running"
                :placeholder="hint"
                spellcheck="false"
              ></textarea>
            </div>

            <div class="actions">
              <button class="btn go" @click="start" :disabled="running || !parsed.length">
                {{ running ? 'building' : 'build bundle' }}<span v-if="running" class="blink"></span>
              </button>
              <span class="hintline">
                {{ parsed.length }} package{{ parsed.length === 1 ? '' : 's' }} ·
                {{ release?.name ?? '—' }}
              </span>
            </div>
            <p v-if="error" class="banner err">{{ error }}</p>
          </div>
        </section>

        <section class="panel" style="--i: 3">
          <h2 class="panel-title">How it works</h2>
          <div class="panel-body">
            <ol class="steps">
              <li>
                <b>Pick the target release.</b> The build runs inside that exact image, so
                dependency resolution matches the machine you are building for.
              </li>
              <li>
                <b>Search, or type names directly.</b> Search asks that release's own package
                manager, so a hit is a package it really ships.
              </li>
              <li>
                <b>Build.</b> Every package plus its dependencies is downloaded and tarred up with
                an <code>install.sh</code>. Identical requests are served from cache instead of
                rebuilt.
              </li>
              <li>
                <b>Carry it over</b> and install offline. No repository access needed on the other
                side.
              </li>
            </ol>
          </div>
        </section>

        <section class="panel" style="--i: 4">
          <h2 class="panel-title">
            On the airgapped machine
            <button class="btn ghost head-action" @click="copy('install', install)">
              {{ copied === 'install' ? 'copied' : 'copy' }}
            </button>
          </h2>
          <div class="panel-body">
            <pre class="snippet">{{ install }}</pre>
            <p class="note">
              Compare <code>sha256sum</code> against the checksum shown next to the bundle before
              you trust it. <code>install.sh</code> installs only the requested packages from the
              bundle's own apt repository on Debian and Ubuntu, or yum repository on Oracle Linux.
              Both run fully offline.
            </p>
          </div>
        </section>
      </div>

      <!-- ── output column ────────────────────────────────────────── -->
      <div class="col">
        <section class="panel console" style="--i: 1">
          <h2 class="panel-title">
            Build log
            <span v-if="job" class="state" :class="job.status">{{ job.status }}</span>
            <span v-if="job?.cached" class="state cache">cache hit</span>
          </h2>
          <div class="panel-body tight">
            <p v-if="job?.error" class="banner err">{{ job.error }}</p>
            <div v-if="job?.status === 'done'" class="result">
              <a class="btn go" :href="`/api/bundles/${job.key}`">download bundle</a>
              <dl>
                <dt>size</dt>
                <dd>{{ size(job.size) }}</dd>
                <dt>sha256</dt>
                <dd class="hash" :title="job.sha256">{{ job.sha256 }}</dd>
              </dl>
              <button class="btn ghost" @click="copy('sha', job.sha256)">
                {{ copied === 'sha' ? 'copied' : 'copy hash' }}
              </button>
            </div>
            <pre ref="logBox" class="log" :class="{ live: running }">{{
              job ? job.log : 'idle — nothing building.\n\nPick a release, choose packages, hit build.'
            }}</pre>
          </div>
        </section>

        <section class="panel" style="--i: 2">
          <h2 class="panel-title">
            Built bundles
            <span class="state">{{
              bundleQuery.trim() ? `${shownBundles.length} / ${bundles.length}` : bundles.length
            }}</span>
          </h2>
          <div class="panel-body tight">
            <p v-if="!bundles.length" class="banner flat">
              Nothing built yet. Finished bundles are kept on disk and listed here, so the same
              request never builds twice.
            </p>
            <input
              v-if="bundles.length"
              v-model="bundleQuery"
              type="search"
              placeholder="filter: ubuntu 24.04 nginx"
              aria-label="Filter built bundles"
            />
            <p v-if="bundles.length && !shownBundles.length" class="banner flat">
              No bundle matches “{{ bundleQuery }}”.
            </p>
            <table v-else-if="bundles.length" class="bundles">
              <tbody>
                <tr v-for="b in pageBundles" :key="b.key">
                  <td class="cell-target">
                    <span class="tgt">{{ b.distro }} {{ b.version }}</span>
                    <span class="pkgs" :title="b.packages.join(' ')">
                      <span v-if="b.repos?.length" class="repotag" :title="b.repos.join(', ')">
                        +{{ b.repos.length }} repo
                      </span>
                      {{ b.packages.join(' ') }}
                    </span>
                  </td>
                  <td class="cell-meta">
                    <span>{{ size(b.size) }}</span>
                    <span class="dim">{{ when(b.created_at) }}</span>
                  </td>
                  <td class="cell-act">
                    <a class="btn tiny" :href="`/api/bundles/${b.key}`">get</a>
                    <button
                      class="btn tiny ghost"
                      :aria-label="`Delete bundle ${b.key}`"
                      @click="forget(b.key)"
                    >
                      ×
                    </button>
                  </td>
                </tr>
              </tbody>
            </table>
            <nav v-if="pages > 1" class="pager" aria-label="Bundle pages">
              <button class="btn tiny ghost" :disabled="bundlePage === 0" @click="bundlePage--">
                ‹ prev
              </button>
              <span class="dim">{{ bundlePage + 1 }} / {{ pages }}</span>
              <button
                class="btn tiny ghost"
                :disabled="bundlePage >= pages - 1"
                @click="bundlePage++"
              >
                next ›
              </button>
            </nav>
          </div>
        </section>
      </div>
    </div>

    <footer>
      <span>bundles and metadata live in <code>BUNDLE_DIR</code> · sqlite</span>
      <span class="dim">
        {{
          synced
            ? `releases read from the registry ${when(synced)}`
            : 'releases from the built-in list'
        }}
        · docker does the resolving · nothing is installed on this host
      </span>
      <span class="author">Dương</span>
    </footer>
  </div>
</template>

<style>
:root {
  color-scheme: dark;
  --bg: #0a0b0d;
  --panel: #101216;
  --panel-2: #14171c;
  --line: #1e222a;
  --line-hot: #2b313b;
  --grid: rgba(255, 255, 255, 0.022);
  --glow: rgba(255, 180, 84, 0.07);
  --focus-ring: rgba(255, 180, 84, 0.12);
  --field: #0c0e11;
  --hover: #12151a;
  --line-hover: #39414e;
  --amber-line: #4a3818;
  --chip-x: #b8873f;
  --placeholder: #545c68;
  --footer-fg: #5c6470;
  /* The go button and the log keep their own pair: amber-on-near-black is the
     wrong contrast once the page turns white. */
  --go-bg: #ffb454;
  --go-bg-hover: #ffc576;
  --go-fg: #17120a;
  --log-bg: #07080a;
  --log-fg: #c3cad3;
  --fg: #e7e9ec;
  --muted: #7c8591;
  --amber: #ffb454;
  --amber-dim: #3a2b12;
  --red: #ff6b6b;
  --red-dim: #2a1416;
  --red-line: #4a2226;
  --green: #7ee787;
  --green-line: #1f4429;
  --mono: 'IBM Plex Mono', ui-monospace, SFMono-Regular, Menlo, monospace;
  --sans: 'IBM Plex Sans Variable', 'IBM Plex Sans', system-ui, sans-serif;
}

/* Light theme: follows the system unless the toggle has pinned one. The block
   is repeated because a media query and an attribute selector cannot be
   combined into one rule. */
@media (prefers-color-scheme: light) {
  :root:not([data-theme='dark']) {
  color-scheme: light;
  --bg: #f4f2ee;
  --panel: #ffffff;
  --panel-2: #f3f0ea;
  --line: #e4e0d7;
  --line-hot: #d5d0c4;
  --fg: #1a1c20;
  --muted: #62666d;
  /* Amber on white fails contrast, so the light theme uses a burnt version of
     it for text and borders and keeps the bright one for filled buttons. */
  --amber: #8a4b00;
  --amber-dim: #fbeed8;
  --amber-line: #e3c48d;
  --red: #a4231c;
  --red-dim: #fdf0ef;
  --red-line: #e8bdba;
  --green: #0a6b40;
  --green-line: #b3ddc6;
  --grid: rgba(0, 0, 0, 0.028);
  --glow: rgba(255, 180, 84, 0.16);
  --focus-ring: rgba(138, 75, 0, 0.16);
  --field: #fbfaf7;
  --hover: #f5f2ec;
  --line-hover: #b9b2a2;
  --chip-x: #a8742f;
  --placeholder: #9a9aa2;
  --footer-fg: #8a8e95;
  --go-bg: #8a4b00;
  --go-bg-hover: #a35a00;
  --go-fg: #fff8ec;
  /* The log and the install snippet stay a terminal in both themes. */
  --log-bg: #14171c;
  --log-fg: #d6dae0;
  }
}
:root[data-theme='light'] {
  color-scheme: light;
  --bg: #f4f2ee;
  --panel: #ffffff;
  --panel-2: #f3f0ea;
  --line: #e4e0d7;
  --line-hot: #d5d0c4;
  --fg: #1a1c20;
  --muted: #62666d;
  /* Amber on white fails contrast, so the light theme uses a burnt version of
     it for text and borders and keeps the bright one for filled buttons. */
  --amber: #8a4b00;
  --amber-dim: #fbeed8;
  --amber-line: #e3c48d;
  --red: #a4231c;
  --red-dim: #fdf0ef;
  --red-line: #e8bdba;
  --green: #0a6b40;
  --green-line: #b3ddc6;
  --grid: rgba(0, 0, 0, 0.028);
  --glow: rgba(255, 180, 84, 0.16);
  --focus-ring: rgba(138, 75, 0, 0.16);
  --field: #fbfaf7;
  --hover: #f5f2ec;
  --line-hover: #b9b2a2;
  --chip-x: #a8742f;
  --placeholder: #9a9aa2;
  --footer-fg: #8a8e95;
  --go-bg: #8a4b00;
  --go-bg-hover: #a35a00;
  --go-fg: #fff8ec;
  /* The log and the install snippet stay a terminal in both themes. */
  --log-bg: #14171c;
  --log-fg: #d6dae0;
}

* { box-sizing: border-box; }

body {
  margin: 0;
  background: var(--bg);
  color: var(--fg);
  font-family: var(--sans);
  font-size: 15px;
  line-height: 1.55;
  -webkit-font-smoothing: antialiased;
  /* A faint grid and one warm glow, so the page reads as a console, not a form. */
  background-image:
    radial-gradient(900px 400px at 12% -8%, var(--glow), transparent 70%),
    linear-gradient(var(--grid) 1px, transparent 1px),
    linear-gradient(90deg, var(--grid) 1px, transparent 1px);
  background-size: 100% 100%, 72px 72px, 72px 72px;
  background-attachment: fixed;
}
body::before {
  content: '';
  position: fixed;
  inset: 0;
  pointer-events: none;
  background: linear-gradient(var(--bg), transparent 30%, transparent 70%, var(--bg));
  z-index: 0;
}

.shell {
  position: relative;
  z-index: 1;
  max-width: 1240px;
  margin: 0 auto;
  padding: 24px 14px calc(40px + env(safe-area-inset-bottom));
}
@media (min-width: 720px) { .shell { padding: 32px 20px 56px; } }
/* A 1440-wide monitor should not show two narrow columns in a sea of grid. */
@media (min-width: 1700px) { .shell { max-width: 1520px; } }

.topbar {
  display: flex;
  align-items: baseline;
  gap: 18px;
  flex-wrap: wrap;
  padding-bottom: 18px;
  margin-bottom: 22px;
  border-bottom: 1px solid var(--line-hot);
}
.brand { display: flex; align-items: center; gap: 2px; }
.mark {
  font-family: var(--mono);
  font-weight: 600;
  font-size: 21px;
  letter-spacing: -0.04em;
  color: var(--fg);
}
.mark::before { content: '▌'; color: var(--amber); margin-right: 6px; }
.caret {
  width: 9px;
  height: 17px;
  background: var(--amber);
  animation: blink 1.15s steps(1) infinite;
}
@keyframes blink { 50% { opacity: 0; } }
.tagline { margin: 0; color: var(--muted); font-size: 13.5px; }
.theme { margin-left: auto; padding: 5px 11px; font-size: 11px; min-height: 0; }

.grid { display: grid; gap: 16px; grid-template-columns: minmax(0, 1fr); }
@media (min-width: 960px) {
  .grid { grid-template-columns: minmax(0, 1.05fr) minmax(0, 1fr); align-items: start; }
  .col:last-child { position: sticky; top: 24px; }
}
.col { display: flex; flex-direction: column; gap: 16px; min-width: 0; }

.panel {
  background: var(--panel);
  border: 1px solid var(--line-hot);
  border-radius: 3px;
  animation: rise 0.5s cubic-bezier(0.2, 0.8, 0.2, 1) backwards;
  animation-delay: calc(var(--i, 0) * 70ms);
}
@keyframes rise { from { opacity: 0; transform: translateY(10px); } }
@media (prefers-reduced-motion: reduce) {
  .panel { animation: none; }
  .caret, .blink { animation: none; }
}

.panel-title {
  display: flex;
  align-items: center;
  gap: 10px;
  margin: 0;
  padding: 11px 16px;
  font-family: var(--mono);
  font-size: 11.5px;
  font-weight: 600;
  letter-spacing: 0.13em;
  text-transform: uppercase;
  color: var(--muted);
  background: var(--panel-2);
  border-bottom: 1px solid var(--line);
}
.panel-body { padding: 18px 16px; }
.panel-body.tight { padding: 12px; }

.two { display: grid; gap: 14px; grid-template-columns: 1fr; }
@media (min-width: 560px) { .two { grid-template-columns: 1fr 1fr; } }

/* One spacing rule for a panel's contents. Adjacent-sibling margins were also
   catching the two fields side by side in .two, pushing Release out of line. */
.panel-body > * + * { margin-top: 16px; }
label {
  display: block;
  margin-bottom: 7px;
  font-family: var(--mono);
  font-size: 11px;
  font-weight: 500;
  letter-spacing: 0.1em;
  text-transform: uppercase;
  color: var(--muted);
}
.count {
  float: right;
  color: var(--amber);
  background: var(--amber-dim);
  padding: 0 7px;
  border-radius: 2px;
}
.count.zero { color: var(--muted); background: var(--field); }
.note { margin: 12px 0 0; color: var(--muted); font-size: 13px; }
code {
  font-family: var(--mono);
  font-size: 0.88em;
  white-space: nowrap;
  background: var(--panel-2);
  border: 1px solid var(--line);
  padding: 1px 5px;
  border-radius: 2px;
  color: var(--amber);
}

select, textarea, input {
  width: 100%;
  padding: 10px 11px;
  font-family: var(--mono);
  font-size: 13.5px;
  color: var(--fg);
  background: var(--field);
  border: 1px solid var(--line-hot);
  border-radius: 2px;
  transition: border-color 0.15s, box-shadow 0.15s;
}
@media (hover: hover) {
  select:hover, textarea:hover, input:hover { border-color: var(--line-hover); }
}
select:focus, textarea:focus, input:focus {
  outline: none;
  border-color: var(--amber);
  box-shadow: 0 0 0 3px var(--focus-ring);
}
select:disabled, textarea:disabled, input:disabled { opacity: 0.45; }
textarea { height: 78px; resize: vertical; line-height: 1.6; }
::placeholder { color: var(--placeholder); }

.searchbar { display: flex; gap: 8px; }
.searchbar input { flex: 1; min-width: 0; }

.btn {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  padding: 9px 15px;
  font-family: var(--mono);
  font-size: 12px;
  font-weight: 500;
  letter-spacing: 0.06em;
  text-transform: lowercase;
  text-decoration: none;
  color: var(--fg);
  background: var(--panel-2);
  border: 1px solid var(--line-hot);
  border-radius: 2px;
  cursor: pointer;
  white-space: nowrap;
  transition: background 0.15s, border-color 0.15s, color 0.15s, transform 0.1s;
}
@media (hover: hover) {
  .btn:hover:not(:disabled) { border-color: var(--amber); color: var(--amber); }
}
.btn:active:not(:disabled) { transform: translateY(1px); }
.btn:disabled { opacity: 0.4; cursor: not-allowed; }
.btn:focus-visible { outline: 2px solid var(--amber); outline-offset: 2px; }
.btn.go {
  background: var(--go-bg);
  border-color: var(--go-bg);
  color: var(--go-fg);
  font-weight: 600;
}
@media (hover: hover) {
  .btn.go:hover:not(:disabled) {
    background: var(--go-bg-hover);
    border-color: var(--go-bg-hover);
    color: var(--go-fg);
  }
      }
.btn.ghost { background: transparent; }
.btn.tiny { padding: 4px 9px; font-size: 11px; }

.blink { width: 7px; height: 13px; background: currentColor; animation: blink 0.9s steps(1) infinite; }

.actions { display: flex; align-items: center; gap: 14px; flex-wrap: wrap; }
.preset { width: auto; flex: 0 1 260px; min-width: 0; font-size: 12px; padding: 8px 9px; }
.repolist { display: flex; flex-direction: column; gap: 8px; }
.repo { display: flex; align-items: flex-start; gap: 8px; flex-wrap: wrap; }
.repo.tall textarea { flex: 1 1 100%; }
.repo input, .repo textarea { flex: 1 1 220px; min-width: 0; font-size: 12.5px; }
.repo textarea { height: auto; line-height: 1.5; resize: vertical; }
.repo .btn { flex: none; }
.hintline { font-family: var(--mono); font-size: 11.5px; color: var(--muted); }

.chips { display: flex; flex-wrap: wrap; gap: 6px; margin-bottom: 9px; }
.chip {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 3px 7px 3px 9px;
  font-family: var(--mono);
  font-size: 12px;
  color: var(--amber);
  background: var(--amber-dim);
  border: 1px solid var(--amber-line);
  border-radius: 2px;
  cursor: pointer;
}
.chip:disabled { cursor: default; opacity: 0.6; }
.chip .x { color: var(--chip-x); font-size: 14px; line-height: 1; }

.results {
  list-style: none;
  margin: 0;
  padding: 0;
  max-height: clamp(180px, 38vh, 320px);
  overflow: auto;
  border: 1px solid var(--line-hot);
  border-radius: 2px;
  background: var(--field);
}
.results li + li { border-top: 1px solid var(--line); }
.results label {
  display: grid;
  grid-template-columns: auto minmax(0, auto) minmax(0, 1fr);
  align-items: center;
  gap: 11px;
  margin: 0;
  padding: 8px 12px;
  font-family: var(--sans);
  font-size: 13.5px;
  letter-spacing: 0;
  text-transform: none;
  color: var(--fg);
  cursor: pointer;
}
.results input[type='checkbox'] { width: 14px; height: 14px; padding: 0; accent-color: var(--amber); }
.pkg { font-family: var(--mono); font-weight: 500; color: var(--amber); }
.versions { padding: 0 12px 8px 37px; font-size: 12px; color: var(--muted); }
.versions summary { cursor: pointer; font-family: var(--mono); }
.versions summary:focus-visible { outline: 2px solid var(--amber); outline-offset: 2px; }
.versions .pinned { margin-left: 8px; color: var(--amber); }
.versions[open] summary { margin-bottom: 4px; }
.versions label {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 0;
  padding: 3px 0;
  font-family: var(--mono);
  font-size: 12px;
  text-transform: none;
  letter-spacing: 0;
  color: var(--fg);
  cursor: pointer;
}
.versions input[type='radio'] { width: 13px; height: 13px; padding: 0; accent-color: var(--amber); }
.summary { color: var(--muted); font-size: 12.5px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

.banner {
  margin: 0;
  padding: 10px 12px;
  border-radius: 2px;
  font-size: 13px;
  border: 1px solid var(--line-hot);
}
.banner.err { color: var(--red); background: var(--red-dim); border-color: var(--red-line); }
.banner.flat { color: var(--muted); background: var(--field); }
.banner b { color: var(--fg); }

.state {
  font-family: var(--mono);
  font-size: 10px;
  letter-spacing: 0.1em;
  padding: 2px 7px;
  border-radius: 2px;
  color: var(--muted);
  background: var(--field);
  border: 1px solid var(--line-hot);
}
.state.running { color: var(--amber); border-color: var(--amber-line); }
.state.done { color: var(--green); border-color: var(--green-line); }
.state.error { color: var(--red); border-color: var(--red-line); }
.state.cache { color: var(--amber); border-color: var(--amber-line); }

.result {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
  padding: 11px 12px;
  background: var(--field);
  border: 1px solid var(--line-hot);
  border-left: 2px solid var(--green);
  border-radius: 2px;
}
.result dl {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr);
  gap: 2px 10px;
  margin: 0;
  font-family: var(--mono);
  font-size: 11.5px;
  min-width: 0;
  flex: 1;
}
.result dt { color: var(--muted); letter-spacing: 0.08em; }
.result dd { margin: 0; color: var(--fg); }
.hash { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--muted) !important; }

.log {
  margin: 0;
  padding: 13px;
  height: clamp(220px, 45vh, 420px);
  overflow: auto;
  background: var(--log-bg);
  border: 1px solid var(--line);
  border-radius: 2px;
  color: var(--log-fg);
  font-family: var(--mono);
  font-size: 12px;
  line-height: 1.5;
  white-space: pre-wrap;
  word-break: break-word;
}
.log.live { border-color: var(--amber-line); }
/* The search log is a progress read-out, not something to scroll through. */
.log.mini { height: clamp(90px, 16vh, 150px); font-size: 11.5px; }

.bundles { width: 100%; border-collapse: collapse; font-size: 13px; }
.pager { display: flex; align-items: center; justify-content: flex-end; gap: 10px; font-size: 12px; }
.bundles tr + tr { border-top: 1px solid var(--line); }
.bundles td { padding: 9px 8px; vertical-align: middle; }
.cell-target { min-width: 0; }
.tgt {
  display: block;
  font-family: var(--mono);
  font-size: 12px;
  color: var(--amber);
}
.repotag {
  display: inline-block;
  margin-right: 5px;
  padding: 0 5px;
  font-family: var(--mono);
  font-size: 10px;
  color: var(--amber);
  background: var(--amber-dim);
  border-radius: 2px;
}
.pkgs {
  display: block;
  color: var(--muted);
  font-size: 12px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 34ch;
}
.cell-meta { white-space: nowrap; font-family: var(--mono); font-size: 11.5px; text-align: right; }
.cell-meta span { display: block; }
.cell-act { white-space: nowrap; text-align: right; }
.cell-act .btn + .btn { margin-left: 5px; }
.dim { color: var(--muted); }

.steps { margin: 0; padding-left: 18px; color: var(--muted); font-size: 13.5px; }
.steps li + li { margin-top: 9px; }
.steps b { color: var(--fg); font-weight: 600; }

.head-action { margin-left: auto; padding: 3px 10px; font-size: 11px; min-height: 0; }
.snippet {
  margin: 0;
  padding: 13px;
  background: var(--log-bg);
  border: 1px solid var(--line);
  border-radius: 2px;
  font-family: var(--mono);
  font-size: 12px;
  line-height: 1.7;
  color: var(--log-fg);
  /* Wrap rather than scroll sideways: in a narrow column the tail of the
     command would otherwise be hidden, and the copy button has the exact text. */
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}


footer {
  display: flex;
  justify-content: space-between;
  gap: 14px;
  flex-wrap: wrap;
  margin-top: 26px;
  padding-top: 16px;
  border-top: 1px solid var(--line);
  font-family: var(--mono);
  font-size: 11px;
  color: var(--footer-fg);
}
footer code { background: none; border: none; padding: 0; }
.author { color: var(--fg); white-space: nowrap; }

/* Touch, at any screen size. iOS Safari zooms the page whenever a control with
   a font under 16px takes focus, and 44px is the smallest target a finger hits
   reliably — the desktop sizes above are neither. */
@media (pointer: coarse) {
  select, textarea, input, .repo input, .repo textarea, .preset { font-size: 16px; }
  .btn, button, select { min-height: 44px; }
  .btn.tiny { min-height: 44px; min-width: 44px; justify-content: center; }
  .results label { padding: 12px; gap: 12px; }
  .results input[type='checkbox'] { width: 18px; height: 18px; }
  .chip { padding: 7px 9px 7px 11px; }
}

/* Narrow screens, whatever is pointing at them: controls stop sharing a row,
   and the package summary gives its width to the name. */
@media (max-width: 620px) {
  .panel-body { padding: 14px 12px; }
  .searchbar { flex-wrap: wrap; }
  .searchbar input { flex: 1 1 100%; }
  .searchbar button { flex: 1 1 auto; justify-content: center; }
  .preset { flex: 1 1 100%; }
  .summary { display: none; }
  .pkgs { max-width: none; }
}
</style>
