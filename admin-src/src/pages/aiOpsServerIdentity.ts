// Display-only formatting. Never rewrite the stored host or SSH parameters.
export function aiServerConnection({ username, host, port }: { username: string; host: string; port: number }): string {
  const displayHost = host.includes(':') && !(host.startsWith('[') && host.endsWith(']')) ? `[${host}]` : host;
  return `${username}@${displayHost}:${port}`;
}
