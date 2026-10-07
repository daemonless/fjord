// A port variable on a network that gives the app its own address does
// nothing: nothing is published on this host, and the app answers on its
// own port at that address. The wizard shows that port instead of a field.

/** insidePort: the container port a "${NAME}:N" mapping publishes to, or "". */
export function insidePort(compose: string, name: string): string {
  const esc = name.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const m = compose.match(new RegExp(`\\$\\{${esc}(?:[:-][^}]*)?\\}:(\\d{2,5})`));
  return m ? m[1] : '';
}

/** ownAddress: the network puts a container on the wire with an address of
 *  its own (fjordd's ownAddress: an epair network). */
export function ownAddress(net: string, networks: { name: string; driver?: string }[]): boolean {
  return !!net && networks.some((n) => n.name === net && n.driver === 'epair');
}
