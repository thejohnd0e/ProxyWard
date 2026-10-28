import { useEffect, useId, useRef, useState } from 'react';

interface Props {
  /** The proxy name, used in the accessible name of the button. */
  name: string;
  onEdit: () => void;
  onDelete: () => void;
  paused: boolean;
  onToggleMonitoring: () => void;
}

interface Place {
  top?: number;
  bottom?: number;
  right: number;
}

const MENU_HEIGHT = 120;

/** Where the menu goes for a button at r: below it, or above when there is no room. */
function placeFor(r: DOMRect): Place {
  const right = Math.max(window.innerWidth - r.right, 8);
  return r.bottom + MENU_HEIGHT + 8 > window.innerHeight ? { bottom: window.innerHeight - r.top + 4, right } : { top: r.bottom + 4, right };
}

/**
 * One "More" button for editing, pausing/resuming monitoring, and deleting. The menu is
 * positioned against the viewport, so the scrolling table cannot clip it.
 */
export function RowMenu({ name, onEdit, onDelete, paused, onToggleMonitoring }: Props) {
  const [place, setPlace] = useState<Place | null>(null);
  const button = useRef<HTMLButtonElement>(null);
  const menu = useRef<HTMLDivElement>(null);
  const menuId = useId();
  const open = place !== null;

  useEffect(() => {
    if (!open) return;
    const items = () => [...(menu.current?.querySelectorAll<HTMLElement>('[role="menuitem"]') ?? [])];
    items()[0]?.focus({ preventScroll: true });
    const onPointer = (e: MouseEvent) => {
      const t = e.target as Node;
      if (!menu.current?.contains(t) && !button.current?.contains(t)) setPlace(null);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.stopPropagation();
        setPlace(null);
        button.current?.focus();
      } else if (e.key === 'Tab') {
        setPlace(null);
      } else if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
        e.preventDefault();
        const list = items();
        const at = list.indexOf(document.activeElement as HTMLElement);
        const step = e.key === 'ArrowDown' ? 1 : -1;
        list[(at + step + list.length) % list.length]?.focus({ preventScroll: true });
      }
    };
    // The menu follows its button when the table or the page scrolls, and
    // closes once the button is out of sight.
    const follow = () => {
      const r = button.current?.getBoundingClientRect();
      if (!r || r.bottom < 0 || r.top > window.innerHeight) setPlace(null);
      else setPlace(placeFor(r));
    };
    document.addEventListener('mousedown', onPointer);
    document.addEventListener('keydown', onKey);
    window.addEventListener('resize', follow);
    window.addEventListener('scroll', follow, true);
    return () => {
      document.removeEventListener('mousedown', onPointer);
      document.removeEventListener('keydown', onKey);
      window.removeEventListener('resize', follow);
      window.removeEventListener('scroll', follow, true);
    };
  }, [open]);

  const toggle = () => {
    if (open) {
      setPlace(null);
      return;
    }
    const r = button.current?.getBoundingClientRect();
    if (!r) return;
    setPlace(placeFor(r));
  };

  const choose = (action: () => void) => {
    setPlace(null);
    action();
  };

  return (
    <>
      <button
        ref={button}
        type="button"
        className="button button--small"
        aria-label={`More actions for ${name}`}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? menuId : undefined}
        onClick={toggle}
      >
        More <span aria-hidden="true">▾</span>
      </button>
      {place ? (
        <div ref={menu} id={menuId} role="menu" aria-label={`Actions for ${name}`} className="menu" style={place}>
          <button type="button" role="menuitem" className="menu__item" onClick={() => choose(onEdit)}>
            Edit
          </button>
          <button type="button" role="menuitem" className="menu__item" onClick={() => choose(onToggleMonitoring)}>
            {paused ? 'Resume' : 'Pause'}
          </button>
          <button type="button" role="menuitem" className="menu__item menu__item--danger" onClick={() => choose(onDelete)}>
            Delete
          </button>
        </div>
      ) : null}
    </>
  );
}
