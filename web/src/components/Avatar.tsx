export function Avatar({
  name,
  url,
  size = 'md',
}: {
  name: string;
  url?: string;
  size?: 'sm' | 'md';
}) {
  const dim = size === 'sm' ? 'size-6' : 'size-8';
  if (url) {
    return (
      <img
        src={url}
        alt=""
        className={`${dim} shrink-0 rounded-full bg-muted object-cover`}
      />
    );
  }
  return (
    <span
      aria-hidden
      className={`${dim} flex shrink-0 items-center justify-center rounded-full bg-muted text-xs font-medium`}
    >
      {name.slice(0, 1).toUpperCase()}
    </span>
  );
}
