import { useCallback, useEffect, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import { api, type ClientTorrent, type ClientTorrentsAnswer, type DebridTorrent, type DebridTorrentsAnswer, type UsenetClientJob, type UsenetClientJobsAnswer } from '../api'
import { formatBytes, timeAgo } from '../format'
import { clientTotals } from '../clientTotals'
import { t, st } from '../i18n'
import { etaText } from '../transferRate'
import { useLive } from '../useLive'
import ActionMenu from './ActionMenu'
import { useConfirm } from './ConfirmProvider'
import Icon from './Icon'
import { useToast } from './Toast'

// A torrent that has moved no data for this long is shown with a warning.
const IDLE_WARN_SECONDS = 30 * 60

// Activity > Download clients, below the built-in ones: what is in the
// external torrent client (qBittorrent, RDT-Client, Decypharr) and on the
// debrid account, with the controls next to each. Administrators only.
export default function DownloadClients() {
  const [client, setClient] = useState<ClientTorrentsAnswer | null>(null)
  const [debrid, setDebrid] = useState<DebridTorrentsAnswer | null>(null)
  const [scope, setScope] = useState<'mine' | 'all'>('mine')
  const [failed, setFailed] = useState('')

  // Only the answer to the latest request is used, so a slow answer for the
  // other scope cannot land on top of the one just asked for.
  const latest = useRef(0)
  const load = useCallback(() => {
    const mine = ++latest.current
    api.clientTorrents(scope).then((a) => {
      if (mine !== latest.current) return
      setClient(a)
      setFailed('')
    }).catch((e) => mine === latest.current && setFailed(e instanceof Error ? e.message : String(e)))
    api.debridTorrents().then(setDebrid).catch(() => undefined)
  }, [scope])
  useEffect(load, [load])
  useLive(load, 4000)

  if (failed) return <p className="error-text">{failed}</p>
  if (!client || !debrid) return <div className="skeleton" style={{ height: 160, borderRadius: 22 }} />
  if (!client.configured && !debrid.configured) {
    return (
      <p className="hint">
        {t('Use qBittorrent, RDT-Client, Decypharr or a debrid service as well? Connect it and it shows up here under the built-in ones.')}{' '}
        <Link to="/settings/torrents">{t('Set it up in Settings')}</Link>
      </p>
    )
  }
  return (
    <div className="download-clients">
      {client.configured && <ClientSection data={client} scope={scope} setScope={setScope} reload={load} />}
      {debrid.configured && <DebridSection data={debrid} reload={load} />}
    </div>
  )
}

// The speed and what is left of everything in a client's list, in its heading.
function TotalsText({ items, up }: { items: { sizeBytes: number; progress: number; downSpeed: number; upSpeed?: number }[]; up?: boolean }) {
  const sum = clientTotals(items)
  const parts = [
    sum.downSpeed > 0 ? `↓ ${formatBytes(sum.downSpeed)}/s` : '',
    up && sum.upSpeed > 0 ? `↑ ${formatBytes(sum.upSpeed)}/s` : '',
    sum.leftBytes > 0 ? t('{size} left', { size: formatBytes(sum.leftBytes) }) : '',
  ].filter(Boolean)
  if (parts.length === 0) return null
  return <span title={t('Added up over everything in the list below')}>{parts.join(' · ')}</span>
}

function ClientSection({ data, scope, setScope, reload }: { data: ClientTorrentsAnswer; scope: 'mine' | 'all'; setScope: (s: 'mine' | 'all') => void; reload: () => void }) {
  const label = data.label ?? t('The download client')
  return (
    <section style={{ marginBottom: 28 }}>
      <div className="rail-head">
        <h2>{label}</h2>
        <span>{data.torrents.length === 1 ? t('1 torrent') : t('{n} torrents', { n: data.torrents.length })}</span>
        <TotalsText items={data.torrents} up />
        <div className="seg" role="group" aria-label={t('Which torrents to show')}>
          <button aria-pressed={scope === 'mine'} className={scope === 'mine' ? 'active' : ''} onClick={() => setScope('mine')} title={t('Only the torrents Mediarium added, the ones in the category "{name}"', { name: data.category ?? '' })}>
            {t('Added by Mediarium')}
          </button>
          <button aria-pressed={scope === 'all'} className={scope === 'all' ? 'active' : ''} onClick={() => setScope('all')}>
            {t('Everything in {name}', { name: label })}
          </button>
        </div>
      </div>
      {data.error && <p className="error-text">{st(data.error)}</p>}
      {!data.error && data.torrents.length === 0 && <p className="hint">{scope === 'mine' ? t('Nothing from Mediarium is in {name} right now.', { name: label }) : t('{name} has no torrents.', { name: label })}</p>}
      {data.torrents.map((tor) => (
        <TorrentRow key={tor.hash} tor={tor} label={label} reload={reload} />
      ))}
    </section>
  )
}

function TorrentRow({ tor, label, reload }: { tor: ClientTorrent; label: string; reload: () => void }) {
  const toast = useToast()
  const confirm = useConfirm()
  const [busy, setBusy] = useState(false)
  const active = tor.stage === 'downloading' || tor.stage === 'waiting' || tor.stage === 'checking'
  const stalled = (tor.stage === 'waiting' || tor.stage === 'downloading') && tor.idleSeconds >= IDLE_WARN_SECONDS

  async function run(action: 'pause' | 'resume' | 'recheck' | 'remove' | 'remove-files', done: string) {
    setBusy(true)
    try {
      await api.clientTorrentAction(tor.hash, action)
      toast.success(done)
      reload()
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  async function remove(files: boolean) {
    const yes = await confirm({
      title: files ? t('Remove {name} and its files?', { name: tor.name }) : t('Remove {name}?', { name: tor.name }),
      body: <p>{tor.mine ? t('Mediarium put this one in {client}. Removing it there cancels that download; use "Give up and search again" if you want another release.', { client: label }) : files ? t('The torrent and the files it downloaded are deleted from {client}.', { client: label }) : t('The torrent is removed from {client}. The files stay on the disk.', { client: label })}</p>,
      confirmLabel: files ? t('Remove and delete files') : t('Remove'),
      danger: true,
    })
    if (yes) await run(files ? 'remove-files' : 'remove', t('Removed.'))
  }

  // For one of Mediarium's own: stop the download, blocklist the release and look for another, and clear the torrent out.
  async function searchAgain() {
    const yes = await confirm({
      title: t('Give up on this one and search again?'),
      body: <p>{t('The release goes on the blocklist, the torrent and its files are removed from {client}, and Mediarium looks for another release.', { client: label })}</p>,
      confirmLabel: t('Give up and search again'),
      danger: true,
    })
    if (!yes || !tor.queueId) return
    setBusy(true)
    try {
      await api.stopQueueItem(tor.queueId, true).catch(() => undefined)
      await api.clientTorrentAction(tor.hash, 'remove-files').catch(() => undefined)
      await api.blocklistQueueItem(tor.queueId)
      toast.success(t('Blocklisted. Looking for another release.'))
      reload()
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  const speed = [tor.downSpeed > 0 ? `↓ ${formatBytes(tor.downSpeed)}/s` : '', tor.upSpeed > 0 ? `↑ ${formatBytes(tor.upSpeed)}/s` : ''].filter(Boolean).join(' · ')
  return (
    <div className={`qrow st-${tor.stage === 'downloading' ? 'downloading' : tor.stage === 'problem' ? 'failed' : tor.stage === 'paused' ? 'paused' : 'queued'} kind-movie`}>
      <div className="qthumb client-thumb" aria-hidden="true">
        <Icon name="download" size={22} />
      </div>
      <div className="qmain">
        <div className="qtitle">
          <span className="client-name" title={tor.name}>{tor.name}</span>
          <span className={`badge ${tor.stage === 'problem' ? 'failed' : tor.stage === 'downloading' ? 'downloading' : ''}`}>{tor.stageText}</span>
          {tor.mine && <span className="qproto">Mediarium</span>}
          {tor.category && !tor.mine && <span className="qproto">{tor.category}</span>}
        </div>
        {(active || tor.stage === 'paused') && (
          <div className="qprogress">
            <div className={`bar ${tor.stage === 'downloading' ? 'active' : ''} ${tor.stage === 'paused' ? 'paused' : ''}`}>
              <span style={{ width: `${Math.max(2, tor.progress)}%` }} />
            </div>
          </div>
        )}
        <div className="qspeed">
          {[
            speed,
            tor.etaSeconds > 0 && etaText(tor.etaSeconds) ? t('about {time} left', { time: etaText(tor.etaSeconds) }) : '',
            tor.stage === 'downloading' || tor.stage === 'waiting' ? t('{seeds} seeds and {peers} peers connected (the swarm has {swarmSeeds} and {swarmPeers})', { seeds: tor.seeds, peers: tor.peers, swarmSeeds: Math.max(0, tor.swarmSeeds), swarmPeers: Math.max(0, tor.swarmPeers) }) : '',
            tor.stage === 'seeding' || tor.stage === 'done' ? t('Shared {ratio} times', { ratio: tor.ratio.toFixed(2) }) : '',
          ]
            .filter(Boolean)
            .join(' · ')}
        </div>
        {stalled && <div className="qnote">{t('No data for {time}. It may be dead.', { time: etaText(tor.idleSeconds) })}</div>}
        <small className="qwhen">
          {tor.addedAt ? t('Added {when}', { when: timeAgo(tor.addedAt) }) : ''}
          {tor.sizeBytes ? `${tor.addedAt ? ' · ' : ''}${formatBytes(tor.sizeBytes)}` : ''}
        </small>
      </div>
      <div className="qmeter" aria-hidden="true">
        <b>
          {tor.progress.toFixed(0)}
          <small>%</small>
        </b>
      </div>
      <div className="row-actions qactions">
        {tor.canPause && (
          <button className="btn-sm btn-with-icon" disabled={busy} onClick={() => void run('pause', t('Paused.'))}>
            <Icon name="pause" size={15} /> {t('Pause')}
          </button>
        )}
        {tor.canResume && (
          <button className="primary btn-sm btn-with-icon" disabled={busy} onClick={() => void run('resume', t('Started again.'))}>
            <Icon name="play" size={15} /> {t('Resume')}
          </button>
        )}
        {tor.mine && tor.queueId && (tor.stage === 'waiting' || tor.stage === 'downloading' || tor.stage === 'problem') && (
          <button className="btn-sm btn-with-icon" disabled={busy} title={t('Blocklist this release and look for another one')} onClick={() => void searchAgain()}>
            <Icon name="ban" size={15} /> {t('Give up and search again')}
          </button>
        )}
        <ActionMenu
          label={t('More')}
          icon="sliders"
          disabled={busy}
          choices={[
            { id: 'recheck', label: t('Check the files again') },
            { id: 'remove', label: t('Remove, keep the files'), danger: true },
            { id: 'remove-files', label: t('Remove and delete the files'), danger: true },
          ]}
          onPick={(id) => {
            if (id === 'recheck') void run('recheck', t('Checking the files again.'))
            else void remove(id === 'remove-files')
          }}
        />
      </div>
    </div>
  )
}

// Activity > Download clients, in the Usenet card: what is in SABnzbd or NZBGet
// when Usenet releases are sent there, with the controls next to each.
// Administrators only. Shows nothing until a downloader is set up.
export function UsenetClients() {
  const [data, setData] = useState<UsenetClientJobsAnswer | null>(null)
  const [scope, setScope] = useState<'mine' | 'all'>('mine')
  const [failed, setFailed] = useState('')
  const latest = useRef(0)
  const load = useCallback(() => {
    const mine = ++latest.current
    api.usenetClientJobs(scope).then((a) => {
      if (mine !== latest.current) return
      setData(a)
      setFailed('')
    }).catch((e) => mine === latest.current && setFailed(e instanceof Error ? e.message : String(e)))
  }, [scope])
  useEffect(load, [load])
  useLive(load, 4000)

  if (failed) return <p className="error-text">{failed}</p>
  if (!data || !data.configured) return null
  const label = data.label ?? t('The Usenet downloader')
  return (
    <section style={{ marginTop: 20 }}>
      <div className="rail-head">
        <h2>{label}</h2>
        <span>{data.jobs.length === 1 ? t('1 download') : t('{n} downloads', { n: data.jobs.length })}</span>
        <TotalsText items={data.jobs} />
        {!data.active && <span className="badge">{t('switched off')}</span>}
        <div className="seg" role="group" aria-label={t('Which downloads to show')}>
          <button aria-pressed={scope === 'mine'} className={scope === 'mine' ? 'active' : ''} onClick={() => setScope('mine')} title={t('Only the downloads Mediarium sent, the ones in the category "{name}"', { name: data.category ?? '' })}>
            {t('Added by Mediarium')}
          </button>
          <button aria-pressed={scope === 'all'} className={scope === 'all' ? 'active' : ''} onClick={() => setScope('all')}>
            {t('Everything in {name}', { name: label })}
          </button>
        </div>
      </div>
      {data.error && <p className="error-text">{st(data.error)}</p>}
      {!data.error && data.jobs.length === 0 && <p className="hint">{scope === 'mine' ? t('Nothing from Mediarium is in {name} right now.', { name: label }) : t('{name} has no downloads.', { name: label })}</p>}
      {data.jobs.map((job) => (
        <UsenetJobRow key={job.id} job={job} label={label} reload={load} />
      ))}
    </section>
  )
}

function stageText(stage: UsenetClientJob['stage']): string {
  switch (stage) {
    case 'downloading':
      return t('Downloading')
    case 'paused':
      return t('Paused')
    case 'processing':
      return t('Checking and unpacking')
    case 'done':
      return t('Finished')
    case 'problem':
      return t('Problem')
  }
  return t('Waiting in line')
}

function UsenetJobRow({ job, label, reload }: { job: UsenetClientJob; label: string; reload: () => void }) {
  const toast = useToast()
  const confirm = useConfirm()
  const [busy, setBusy] = useState(false)
  const active = job.stage === 'downloading' || job.stage === 'queued' || job.stage === 'processing'

  async function run(action: 'pause' | 'resume' | 'remove' | 'remove-files', done: string) {
    setBusy(true)
    try {
      await api.usenetClientJobAction(job.id, action)
      toast.success(done)
      reload()
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  async function remove(files: boolean) {
    const yes = await confirm({
      title: files ? t('Remove {name} and its files?', { name: job.name }) : t('Remove {name}?', { name: job.name }),
      body: <p>{job.mine ? t('Mediarium put this one in {client}. Removing it there cancels that download; use "Give up and search again" if you want another release.', { client: label }) : files ? t('The download and the files it made are deleted from {client}.', { client: label }) : t('The download is removed from {client}. The files stay on the disk.', { client: label })}</p>,
      confirmLabel: files ? t('Remove and delete files') : t('Remove'),
      danger: true,
    })
    if (yes) await run(files ? 'remove-files' : 'remove', t('Removed.'))
  }

  async function searchAgain() {
    const yes = await confirm({
      title: t('Give up on this one and search again?'),
      body: <p>{t('The release goes on the blocklist, the download and its files are removed from {client}, and Mediarium looks for another release.', { client: label })}</p>,
      confirmLabel: t('Give up and search again'),
      danger: true,
    })
    if (!yes || !job.queueId) return
    setBusy(true)
    try {
      await api.stopQueueItem(job.queueId, true).catch(() => undefined)
      await api.usenetClientJobAction(job.id, 'remove-files').catch(() => undefined)
      await api.blocklistQueueItem(job.queueId)
      toast.success(t('Blocklisted. Looking for another release.'))
      reload()
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  const tone = job.stage === 'downloading' ? 'downloading' : job.stage === 'problem' ? 'failed' : job.stage === 'paused' ? 'paused' : 'queued'
  return (
    <div className={`qrow st-${tone} kind-movie`}>
      <div className="qthumb client-thumb" aria-hidden="true">
        <Icon name="download" size={22} />
      </div>
      <div className="qmain">
        <div className="qtitle">
          <span className="client-name" title={job.name}>{job.name}</span>
          <span className={`badge ${job.stage === 'problem' ? 'failed' : job.stage === 'downloading' ? 'downloading' : ''}`}>{stageText(job.stage)}</span>
          {job.mine && <span className="qproto">Mediarium</span>}
          {job.category && !job.mine && <span className="qproto">{job.category}</span>}
        </div>
        {(active || job.stage === 'paused') && (
          <div className="qprogress">
            <div className={`bar ${job.stage === 'downloading' ? 'active' : ''} ${job.stage === 'paused' ? 'paused' : ''}`}>
              <span style={{ width: `${Math.max(2, job.progress)}%` }} />
            </div>
          </div>
        )}
        <div className="qspeed">
          {[job.downSpeed > 0 ? `↓ ${formatBytes(job.downSpeed)}/s` : '', job.etaSeconds > 0 && etaText(job.etaSeconds) ? t('about {time} left', { time: etaText(job.etaSeconds) }) : ''].filter(Boolean).join(' · ')}
        </div>
        {job.stage === 'problem' && job.failMessage && <div className="qnote">{job.failMessage}</div>}
        <small className="qwhen">
          {job.addedAt ? t('Added {when}', { when: timeAgo(job.addedAt) }) : ''}
          {job.sizeBytes ? `${job.addedAt ? ' · ' : ''}${formatBytes(job.sizeBytes)}` : ''}
        </small>
      </div>
      <div className="qmeter" aria-hidden="true">
        <b>
          {job.progress.toFixed(0)}
          <small>%</small>
        </b>
      </div>
      <div className="row-actions qactions">
        {job.canPause && (
          <button className="btn-sm btn-with-icon" disabled={busy} onClick={() => void run('pause', t('Paused.'))}>
            <Icon name="pause" size={15} /> {t('Pause')}
          </button>
        )}
        {job.canResume && (
          <button className="primary btn-sm btn-with-icon" disabled={busy} onClick={() => void run('resume', t('Started again.'))}>
            <Icon name="play" size={15} /> {t('Resume')}
          </button>
        )}
        {job.mine && job.queueId && (job.stage === 'queued' || job.stage === 'downloading' || job.stage === 'problem') && (
          <button className="btn-sm btn-with-icon" disabled={busy} title={t('Blocklist this release and look for another one')} onClick={() => void searchAgain()}>
            <Icon name="ban" size={15} /> {t('Give up and search again')}
          </button>
        )}
        <ActionMenu
          label={t('More')}
          icon="sliders"
          disabled={busy}
          choices={[
            { id: 'remove', label: t('Remove, keep the files'), danger: true },
            { id: 'remove-files', label: t('Remove and delete the files'), danger: true },
          ]}
          onPick={(id) => void remove(id === 'remove-files')}
        />
      </div>
    </div>
  )
}

function DebridSection({ data, reload }: { data: { label?: string; error?: string; torrents: DebridTorrent[] }; reload: () => void }) {
  const label = data.label ?? t('The debrid service')
  const toast = useToast()
  const confirm = useConfirm()
  const [busy, setBusy] = useState('')

  async function remove(d: DebridTorrent) {
    const yes = await confirm({ title: t('Remove {name} from {service}?', { name: d.name, service: label }), body: <p>{t('It is deleted from your account there. Files Mediarium already downloaded are not touched.')}</p>, confirmLabel: t('Remove'), danger: true })
    if (!yes) return
    setBusy(d.id)
    try {
      await api.debridRemove(d.id)
      toast.success(t('Removed.'))
      reload()
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy('')
    }
  }

  return (
    <section>
      <div className="rail-head">
        <h2>{label}</h2>
        <span>{data.torrents.length === 1 ? t('1 torrent') : t('{n} torrents', { n: data.torrents.length })}</span>
      </div>
      {data.error && <p className="error-text">{st(data.error)}</p>}
      {!data.error && data.torrents.length === 0 && <p className="hint">{t('{name} has no torrents.', { name: label })}</p>}
      {data.torrents.map((d) => (
        <div key={d.id} className={`qrow st-${d.stage === 'downloading' ? 'downloading' : d.stage === 'problem' ? 'failed' : 'queued'} kind-movie`}>
          <div className="qthumb client-thumb" aria-hidden="true">
            <Icon name="download" size={22} />
          </div>
          <div className="qmain">
            <div className="qtitle">
              <span className="client-name" title={d.name}>{d.name}</span>
              <span className={`badge ${d.stage === 'problem' ? 'failed' : d.stage === 'downloading' ? 'downloading' : ''}`}>{d.stageText}</span>
            </div>
            {d.stage !== 'ready' && d.stage !== 'problem' && (
              <div className="qprogress">
                <div className={`bar ${d.stage === 'downloading' ? 'active' : ''}`}>
                  <span style={{ width: `${Math.max(2, d.progress)}%` }} />
                </div>
              </div>
            )}
            <div className="qspeed">{[d.downSpeed > 0 ? `↓ ${formatBytes(d.downSpeed)}/s` : '', d.seeders >= 0 ? t('{n} seeders', { n: d.seeders }) : ''].filter(Boolean).join(' · ')}</div>
            <small className="qwhen">
              {d.addedAt ? t('Added {when}', { when: timeAgo(d.addedAt) }) : ''}
              {d.sizeBytes ? `${d.addedAt ? ' · ' : ''}${formatBytes(d.sizeBytes)}` : ''}
            </small>
          </div>
          <div className="qmeter" aria-hidden="true">
            <b>
              {d.progress.toFixed(0)}
              <small>%</small>
            </b>
          </div>
          <div className="row-actions qactions">
            <button className="icon-btn danger" title={t('Remove from {service}', { service: label })} aria-label={t('Remove from {service}', { service: label })} disabled={busy === d.id} onClick={() => void remove(d)}>
              <Icon name="trash" size={17} />
            </button>
          </div>
        </div>
      ))}
    </section>
  )
}
