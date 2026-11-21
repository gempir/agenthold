import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  CheckCircle2,
  CircleAlert,
  Hammer,
  Layers,
  Loader2,
  Pencil,
  Plus,
  ScrollText,
  Star,
  Trash2,
  X,
} from 'lucide-react';
import { api, unwrap } from '@/api/client';
import {
  imageBuildQuery,
  imageQuery,
  imagesQuery,
  meQuery,
  type CustomImage,
  type ImageBuild,
  type ImageInput,
} from '@/api/queries';
import { secretsQuery } from '@/components/SecretsPanel';
import { ErrorText, Loading } from '@/components/Spinner';
import { SettingsHeader } from '@/components/layout/SettingsLayout';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { Input } from '@/components/ui/input';

function formatBytes(n: number) {
  if (!n) return '—';
  if (n < 1024 ** 2) return `${(n / 1024).toFixed(0)} KiB`;
  if (n < 1024 ** 3) return `${(n / 1024 ** 2).toFixed(1)} MiB`;
  return `${(n / 1024 ** 3).toFixed(2)} GiB`;
}

function parsePackages(raw: string): string[] {
  return raw
    .split(/[\s,]+/)
    .map((s) => s.trim())
    .filter(Boolean);
}

/** VM images: the built-in agenthold Base and custom layered images. */
export function ImagesPage() {
  const me = useQuery(meQuery);
  const catalog = useQuery(imagesQuery);
  const qc = useQueryClient();
  const [editing, setEditing] = useState<'new' | CustomImage | null>(null);
  const [viewingBuilds, setViewingBuilds] = useState<{
    image: CustomImage;
    buildId?: string;
  } | null>(null);

  const isAdmin = !!me.data?.admin;

  const refresh = async (imageId?: string) => {
    await qc.invalidateQueries({ queryKey: imagesQuery.queryKey });
    if (imageId) {
      await qc.invalidateQueries({ queryKey: ['images', imageId] });
    }
  };

  const buildMut = useMutation({
    mutationFn: (imageId: string) =>
      unwrap(
        api.POST('/images/{imageId}/builds', {
          params: { path: { imageId } },
        }),
      ),
    onSuccess: async (builds, imageId) => {
      await refresh(imageId);
      const img = catalog.data?.images.find((i) => i.id === imageId);
      if (img) {
        setViewingBuilds({ image: img, buildId: builds[0]?.id });
      }
    },
  });

  const defaultMut = useMutation({
    mutationFn: ({
      image,
      isDefault,
    }: {
      image: CustomImage;
      isDefault: boolean;
    }) =>
      unwrap(
        api.PATCH('/images/{imageId}', {
          params: { path: { imageId: image.id } },
          body: {
            name: image.name,
            description: image.description,
            aptPackages: image.aptPackages,
            setupScript: image.setupScript,
            env: image.env,
            secretIds: image.secretIds,
            isDefault,
          },
        }),
      ),
    onSuccess: () => refresh(),
  });

  const deleteMut = useMutation({
    mutationFn: (imageId: string) =>
      unwrap(
        api.DELETE('/images/{imageId}', {
          params: { path: { imageId } },
        }),
      ),
    onSuccess: () => refresh(),
  });

  if (me.isPending || catalog.isPending) return <Loading />;

  const base = catalog.data?.base;
  const images = catalog.data?.images ?? [];

  return (
    <>
      <SettingsHeader
        title="Images"
        description="VM images for agents and apps. Custom images build as read-only layers on top of agenthold Base and sync privately over mTLS to connected nodes."
      />
      <ErrorText
        error={
          catalog.error ?? buildMut.error ?? defaultMut.error ?? deleteMut.error
        }
      />

      {base && (
        <section className="mb-6 grid gap-2" aria-label="Base image">
          <h2 className="text-sm font-medium text-muted-foreground">
            Base image
          </h2>
          <div className="rounded-xl border p-4">
            <div className="flex flex-wrap items-center gap-2">
              <Layers className="size-4 text-muted-foreground" aria-hidden />
              <strong>{base.name}</strong>
              <Badge variant="outline">Built-in</Badge>
              {base.isDefault && <Badge>Default</Badge>}
            </div>
            <p className="mt-1.5 text-sm text-muted-foreground">
              {base.description}
            </p>
          </div>
        </section>
      )}

      <section className="grid gap-3" aria-label="Custom images">
        <div className="flex items-center justify-between gap-2">
          <h2 className="text-sm font-medium text-muted-foreground">
            Custom images
          </h2>
          {isAdmin && (
            <Button size="sm" onClick={() => setEditing('new')}>
              <Plus className="size-4" />
              New image
            </Button>
          )}
        </div>

        {images.length === 0 ? (
          <p className="rounded-xl border border-dashed p-6 text-center text-sm text-muted-foreground">
            {isAdmin
              ? 'No custom images yet. Create one to bake APT packages, toolchains, scripts, and environment variables into a reusable VM layer.'
              : 'No custom images have been configured by an admin yet.'}
          </p>
        ) : (
          <ul aria-label="Custom images list" className="grid gap-3">
            {images.map((img) => (
              <li key={img.id} className="rounded-xl border p-4">
                <div className="flex flex-wrap items-start justify-between gap-2">
                  <div className="min-w-0">
                    <div className="flex flex-wrap items-center gap-2">
                      <strong>{img.name}</strong>
                      {img.isDefault && <Badge>Default</Badge>}
                      {img.latestRevision > 0 ? (
                        <Badge variant="secondary">
                          rev {img.latestRevision}
                        </Badge>
                      ) : (
                        <Badge variant="outline">Not built</Badge>
                      )}
                      {img.building && (
                        <Badge
                          variant="outline"
                          className="gap-1 border-amber-500/50 text-amber-600 dark:text-amber-400"
                        >
                          <Loader2 className="size-3 animate-spin" />
                          Building
                        </Badge>
                      )}
                      {!img.building && img.latestBuildStatus === 'failed' && (
                        <Badge
                          variant="outline"
                          className="gap-1 border-destructive/50 text-destructive"
                        >
                          <CircleAlert className="size-3" />
                          Last build failed
                        </Badge>
                      )}
                      {img.readyArches.map((arch) => (
                        <Badge key={arch} variant="outline">
                          {arch}
                        </Badge>
                      ))}
                    </div>
                    {img.description && (
                      <p className="mt-1 text-sm text-muted-foreground">
                        {img.description}
                      </p>
                    )}
                    <div className="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground">
                      <span>
                        {img.agentCount}{' '}
                        {img.agentCount === 1 ? 'machine' : 'machines'}
                      </span>
                      {img.aptPackages.length > 0 && (
                        <span>
                          APT: {img.aptPackages.slice(0, 5).join(', ')}
                          {img.aptPackages.length > 5
                            ? ` +${img.aptPackages.length - 5}`
                            : ''}
                        </span>
                      )}
                      {Object.keys(img.env).length > 0 && (
                        <span>
                          {Object.keys(img.env).length} env{' '}
                          {Object.keys(img.env).length === 1 ? 'var' : 'vars'}
                        </span>
                      )}
                    </div>
                  </div>
                  {isAdmin && (
                    <div className="flex flex-wrap items-center gap-1.5">
                      <Button
                        size="sm"
                        disabled={buildMut.isPending || img.building}
                        onClick={() => buildMut.mutate(img.id)}
                      >
                        <Hammer className="size-3.5" />
                        Build
                      </Button>
                      <Button
                        size="sm"
                        variant="outline"
                        onClick={() => setViewingBuilds({ image: img })}
                      >
                        <ScrollText className="size-3.5" />
                        Logs
                      </Button>
                      <Button
                        size="sm"
                        variant="outline"
                        onClick={() => setEditing(img)}
                      >
                        <Pencil className="size-3.5" />
                        Edit
                      </Button>
                      {img.latestRevision > 0 && (
                        <Button
                          size="sm"
                          variant="ghost"
                          disabled={defaultMut.isPending}
                          title={
                            img.isDefault
                              ? 'Reset default back to agenthold Base'
                              : 'Make default for new agents and apps'
                          }
                          onClick={() =>
                            defaultMut.mutate({
                              image: img,
                              isDefault: !img.isDefault,
                            })
                          }
                        >
                          <Star
                            className={
                              img.isDefault
                                ? 'size-3.5 fill-current'
                                : 'size-3.5'
                            }
                          />
                          {img.isDefault ? 'Unset default' : 'Set default'}
                        </Button>
                      )}
                      <Button
                        size="sm"
                        variant="ghost"
                        aria-label={`Delete ${img.name}`}
                        disabled={deleteMut.isPending || img.agentCount > 0}
                        title={
                          img.agentCount > 0
                            ? 'Switch or delete machines using this image first'
                            : 'Delete image'
                        }
                        onClick={() => {
                          if (
                            confirm(
                              `Delete custom image "${img.name}" and its built layers?`,
                            )
                          ) {
                            deleteMut.mutate(img.id);
                          }
                        }}
                      >
                        <Trash2 className="size-3.5" />
                      </Button>
                    </div>
                  )}
                </div>
              </li>
            ))}
          </ul>
        )}
      </section>

      {editing && (
        <ImageEditorDialog
          initial={editing === 'new' ? null : editing}
          onClose={() => setEditing(null)}
          onSaved={async (saved, startBuild) => {
            setEditing(null);
            await refresh(saved.id);
            if (startBuild) {
              buildMut.mutate(saved.id);
            }
          }}
        />
      )}

      {viewingBuilds && (
        <ImageBuildsDialog
          imageId={viewingBuilds.image.id}
          imageName={viewingBuilds.image.name}
          initialBuildId={viewingBuilds.buildId}
          onClose={() => setViewingBuilds(null)}
          onBuildNow={() => buildMut.mutate(viewingBuilds.image.id)}
        />
      )}
    </>
  );
}

function ImageEditorDialog({
  initial,
  onClose,
  onSaved,
}: {
  initial: CustomImage | null;
  onClose: () => void;
  onSaved: (image: CustomImage, startBuild: boolean) => void | Promise<void>;
}) {
  const secrets = useQuery(secretsQuery).data ?? [];
  const [name, setName] = useState(initial?.name ?? '');
  const [description, setDescription] = useState(initial?.description ?? '');
  const [aptRaw, setAptRaw] = useState((initial?.aptPackages ?? []).join(' '));
  const [setupScript, setSetupScript] = useState(initial?.setupScript ?? '');
  const [envRows, setEnvRows] = useState<{ key: string; value: string }[]>(
    () => {
      const entries = Object.entries(initial?.env ?? {});
      return entries.map(([key, value]) => ({ key, value }));
    },
  );
  const [secretIds, setSecretIds] = useState<string[]>(
    initial?.secretIds ?? [],
  );
  const [isDefault, setIsDefault] = useState(initial?.isDefault ?? false);
  const [buildAfter, setBuildAfter] = useState(false);

  const save = useMutation({
    mutationFn: (body: ImageInput) =>
      initial
        ? unwrap(
            api.PATCH('/images/{imageId}', {
              params: { path: { imageId: initial.id } },
              body,
            }),
          )
        : unwrap(api.POST('/images', { body })),
    onSuccess: (saved) => onSaved(saved, buildAfter),
  });

  const submit = (andBuild: boolean) => {
    setBuildAfter(andBuild);
    const env: Record<string, string> = {};
    for (const row of envRows) {
      const k = row.key.trim();
      if (k) env[k] = row.value;
    }
    save.mutate({
      name: name.trim(),
      description: description.trim(),
      aptPackages: parsePackages(aptRaw),
      setupScript,
      env,
      secretIds,
      isDefault,
    });
  };

  const toggleSecret = (id: string) =>
    setSecretIds((prev) =>
      prev.includes(id) ? prev.filter((x) => x !== id) : [...prev, id],
    );

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[90vh] max-w-2xl overflow-y-auto">
        <DialogHeader>
          <DialogTitle>
            {initial ? `Edit ${initial.name}` : 'New custom image'}
          </DialogTitle>
          <DialogDescription>
            Builds a read-only ext4 layer on top of agenthold Base inside an
            isolated builder microVM.
          </DialogDescription>
        </DialogHeader>
        <form
          className="grid gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            submit(false);
          }}
        >
          <label className="grid gap-1 text-sm">
            Name
            <Input
              required
              autoFocus
              placeholder="e.g. Python & Data"
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
          </label>
          <label className="grid gap-1 text-sm">
            Description (optional)
            <Input
              placeholder="Preinstalled packages and toolchains for this team"
              value={description}
              onChange={(e) => setDescription(e.target.value)}
            />
          </label>
          <label className="grid gap-1 text-sm">
            APT packages (optional)
            <Input
              placeholder="ripgrep jq python3-venv postgresql-client"
              value={aptRaw}
              onChange={(e) => setAptRaw(e.target.value)}
            />
            <span className="text-xs text-muted-foreground">
              Space- or comma-separated Debian package names installed with{' '}
              <code>apt-get install --no-install-recommends</code>.
            </span>
          </label>
          <label className="grid gap-1 text-sm">
            Setup script (bash, runs as root)
            <textarea
              rows={7}
              className="w-full rounded-md border border-input bg-background px-3 py-2 font-mono text-xs ring-offset-background placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              placeholder={
                '# Example:\ncurl -LsSf https://astral.sh/uv/install.sh | env UV_INSTALL_DIR=/usr/local/bin sh'
              }
              value={setupScript}
              onChange={(e) => setSetupScript(e.target.value)}
            />
            <span className="text-xs text-muted-foreground">
              Install tools under <code>/opt</code> or <code>/usr/local</code>,
              and default dotfiles under <code>/etc/skel</code> (at runtime{' '}
              <code>/home/agent</code> is each machine&apos;s persistent home
              disk).
            </span>
          </label>

          <div className="grid gap-2">
            <div className="flex items-center justify-between">
              <span className="text-sm font-medium">Environment variables</span>
              <Button
                type="button"
                size="sm"
                variant="outline"
                onClick={() =>
                  setEnvRows((r) => [...r, { key: '', value: '' }])
                }
              >
                <Plus className="size-3.5" />
                Add variable
              </Button>
            </div>
            {envRows.length > 0 && (
              <div className="grid gap-2">
                {envRows.map((row, idx) => (
                  <div key={idx} className="flex items-center gap-2">
                    <Input
                      aria-label={`Env key ${idx + 1}`}
                      placeholder="KEY (e.g. PATH)"
                      className="w-44 font-mono text-xs"
                      value={row.key}
                      onChange={(e) =>
                        setEnvRows((rows) =>
                          rows.map((r, i) =>
                            i === idx ? { ...r, key: e.target.value } : r,
                          ),
                        )
                      }
                    />
                    <Input
                      aria-label={`Env value ${idx + 1}`}
                      placeholder="VALUE (e.g. /opt/tools/bin:$PATH)"
                      className="flex-1 font-mono text-xs"
                      value={row.value}
                      onChange={(e) =>
                        setEnvRows((rows) =>
                          rows.map((r, i) =>
                            i === idx ? { ...r, value: e.target.value } : r,
                          ),
                        )
                      }
                    />
                    <Button
                      type="button"
                      size="icon"
                      variant="ghost"
                      aria-label="Remove variable"
                      onClick={() =>
                        setEnvRows((rows) => rows.filter((_, i) => i !== idx))
                      }
                    >
                      <X className="size-4" />
                    </Button>
                  </div>
                ))}
              </div>
            )}
            <span className="text-xs text-muted-foreground">
              Written to <code>/etc/agenthold/image.env.d/10-custom.env</code>.{' '}
              <code>PATH</code> entries support <code>$PATH</code> expansion.
            </span>
          </div>

          {secrets.length > 0 && (
            <fieldset className="grid gap-1.5">
              <legend className="text-sm font-medium">
                Build-time secrets (optional)
              </legend>
              <span className="text-xs text-muted-foreground">
                Injected on the wire by the egress proxy only during builds,
                never written into the resulting image.
              </span>
              <div className="mt-1 grid gap-1.5 rounded-lg border p-2.5">
                {secrets.map((s) => (
                  <label key={s.id} className="flex items-center gap-2 text-sm">
                    <input
                      type="checkbox"
                      checked={secretIds.includes(s.id)}
                      onChange={() => toggleSecret(s.id)}
                    />
                    <span className="font-mono text-xs">{s.name}</span>
                  </label>
                ))}
              </div>
            </fieldset>
          )}

          <label className="flex items-center gap-2 text-sm">
            <input
              type="checkbox"
              checked={isDefault}
              onChange={(e) => setIsDefault(e.target.checked)}
            />
            Use as installation default for new agents and apps
          </label>

          <ErrorText error={save.error} />
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              Cancel
            </Button>
            <Button
              type="submit"
              variant="outline"
              disabled={!name.trim() || save.isPending}
            >
              Save
            </Button>
            <Button
              type="button"
              disabled={!name.trim() || save.isPending}
              onClick={() => submit(true)}
            >
              <Hammer className="size-4" />
              Save &amp; build
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function ImageBuildsDialog({
  imageId,
  imageName,
  initialBuildId,
  onClose,
  onBuildNow,
}: {
  imageId: string;
  imageName: string;
  initialBuildId?: string;
  onClose: () => void;
  onBuildNow: () => void;
}) {
  const detail = useQuery(imageQuery(imageId));
  const builds = detail.data?.builds ?? [];
  const [selectedId, setSelectedId] = useState<string | undefined>(
    initialBuildId,
  );
  const activeId =
    selectedId && builds.some((b) => b.id === selectedId)
      ? selectedId
      : builds[0]?.id;

  const activeBuild = useQuery({
    ...imageBuildQuery(imageId, activeId ?? ''),
    enabled: !!activeId,
  });

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[90vh] max-w-3xl overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{imageName} — Builds</DialogTitle>
          <DialogDescription>
            Each build runs inside a fresh Firecracker microVM per connected
            architecture and produces an immutable read-only ext4 layer.
          </DialogDescription>
        </DialogHeader>

        <ErrorText error={detail.error ?? activeBuild.error} />
        {detail.isPending && <Loading />}

        {detail.data && builds.length === 0 && (
          <div className="grid justify-items-center gap-3 rounded-lg border border-dashed p-6 text-center text-sm text-muted-foreground">
            <p>No builds yet for this image.</p>
            <Button size="sm" onClick={onBuildNow}>
              <Hammer className="size-3.5" />
              Start first build
            </Button>
          </div>
        )}

        {builds.length > 0 && (
          <div className="grid gap-4">
            <div className="flex flex-wrap gap-2" role="tablist">
              {builds.map((b) => (
                <BuildPill
                  key={b.id}
                  build={b}
                  selected={b.id === activeId}
                  onSelect={() => setSelectedId(b.id)}
                />
              ))}
            </div>

            {activeBuild.isPending && <Loading />}
            {activeBuild.data && (
              <div className="grid gap-2">
                <div className="flex flex-wrap items-center gap-3 text-xs text-muted-foreground">
                  <span>
                    Revision <strong>{activeBuild.data.revision}</strong>
                  </span>
                  <span>Arch: {activeBuild.data.arch}</span>
                  <span>Status: {activeBuild.data.status}</span>
                  {activeBuild.data.layerSize > 0 && (
                    <span>
                      Size: {formatBytes(activeBuild.data.layerSize)} raw (
                      {formatBytes(activeBuild.data.zstdSize)} zstd)
                    </span>
                  )}
                </div>
                {activeBuild.data.error && (
                  <p
                    role="alert"
                    className="rounded-lg border border-destructive/30 bg-destructive/5 p-2.5 text-sm text-destructive"
                  >
                    {activeBuild.data.error}
                  </p>
                )}
                <pre
                  aria-label="Build log"
                  className="max-h-96 overflow-auto rounded-lg border bg-zinc-950 p-3 font-mono text-xs text-zinc-100 whitespace-pre-wrap"
                >
                  {activeBuild.data.log ||
                    (activeBuild.data.status === 'queued'
                      ? 'Waiting for builder node...'
                      : 'No build output yet.')}
                </pre>
              </div>
            )}
          </div>
        )}

        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            Close
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function BuildPill({
  build,
  selected,
  onSelect,
}: {
  build: ImageBuild;
  selected: boolean;
  onSelect: () => void;
}) {
  return (
    <button
      type="button"
      role="tab"
      aria-selected={selected}
      onClick={onSelect}
      className={`inline-flex items-center gap-1.5 rounded-md border px-2.5 py-1 text-xs font-medium transition-colors ${
        selected
          ? 'border-primary bg-primary/10 text-foreground'
          : 'border-border text-muted-foreground hover:bg-accent'
      }`}
    >
      {build.status === 'ready' && (
        <CheckCircle2 className="size-3.5 text-emerald-600 dark:text-emerald-400" />
      )}
      {build.status === 'failed' && (
        <CircleAlert className="size-3.5 text-destructive" />
      )}
      {(build.status === 'queued' || build.status === 'building') && (
        <Loader2 className="size-3.5 animate-spin text-amber-600 dark:text-amber-400" />
      )}
      <span>
        rev {build.revision} · {build.arch}
      </span>
    </button>
  );
}
