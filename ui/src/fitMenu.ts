// A dropdown that hangs from its button's left edge. Buttons near the right
// of a header (Open ▾, the restart policy, Sign in) grew menus off the
// window: the rename box lost its ending and its Save. This opens it
// leftwards when rightwards would not fit, and keeps it no wider than the
// window. Measured again whenever its size changes, which covers a
// <details> menu (zero-sized while closed) and content that swaps.
export function fitMenu(node: HTMLElement) {
  node.style.maxWidth = 'calc(100vw - 1rem)';
  const place = () => {
    node.style.left = '';
    node.style.right = '';
    const r = node.getBoundingClientRect();
    if (r.width > 0 && r.right > window.innerWidth - 8) {
      node.style.left = 'auto';
      node.style.right = '0';
    }
  };
  place();
  const ro = new ResizeObserver(place);
  ro.observe(node);
  window.addEventListener('resize', place);
  return {
    destroy() {
      ro.disconnect();
      window.removeEventListener('resize', place);
    },
  };
}
