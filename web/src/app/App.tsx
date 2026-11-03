import { useState } from 'react';
import { NavLink, Navigate, Outlet, Route, Routes } from 'react-router-dom';
import { LoginPage } from '../features/auth/LoginPage';
import { SetupPage } from '../features/auth/SetupPage';
import { EventPage } from '../features/events/EventPage';
import { OverviewPage } from '../features/overview/OverviewPage';
import { ProxyDetail } from '../features/proxies/ProxyDetail';
import { ProxyPage } from '../features/proxies/ProxyPage';
import { SettingsPage } from '../features/settings/SettingsPage';
import { Dialog } from '../components/Dialog';
import { useAuth } from './auth';
import { useTheme } from './theme';

function Icon({ name }: { name: 'overview' | 'proxies' | 'events' }) {
  if (name === 'overview') {
    return (
      <svg className="nav-icon" viewBox="0 0 24 24" aria-hidden="true" focusable="false">
        <rect x="3" y="3" width="7.5" height="7.5" rx="1.6" />
        <rect x="13.5" y="3" width="7.5" height="4.5" rx="1.6" />
        <rect x="13.5" y="10.5" width="7.5" height="10.5" rx="1.6" />
        <rect x="3" y="13.5" width="7.5" height="7.5" rx="1.6" />
      </svg>
    );
  }
  if (name === 'proxies') {
    return (
      <svg className="nav-icon" viewBox="0 0 24 24" aria-hidden="true" focusable="false">
        <rect x="3" y="4" width="18" height="6" rx="1.6" />
        <rect x="3" y="14" width="18" height="6" rx="1.6" />
        <path d="M7 7h.01M7 17h.01" />
      </svg>
    );
  }
  return (
    <svg className="nav-icon" viewBox="0 0 24 24" aria-hidden="true" focusable="false">
      <path d="M3 12h4l2.5-6.5 4 13L16 12h5" />
    </svg>
  );
}

function Shell() {
  const [settingsOpen, setSettingsOpen] = useState(false);
  return (
    <div className="app">
      <aside className="sidebar">
        <header className="sidebar__head">
          <div className="brand">
            ProxyWard <span className="brand__version">v{__APP_VERSION__}</span>
          </div>
          <button
            type="button"
            className="icon-button settings-button"
            aria-label="Settings"
            onClick={() => setSettingsOpen(true)}
          >
            <svg viewBox="0 0 24 24" aria-hidden="true" focusable="false">
              <path d="M12 8a4 4 0 1 0 0 8 4 4 0 0 0 0-8Z" />
              <path d="M19.4 13.5a7.6 7.6 0 0 0 0-3l1.7-1.3-1.8-3.1-2 .8a7.4 7.4 0 0 0-2.6-1.5L14.3 3h-3.6l-.4 2.4a7.4 7.4 0 0 0-2.6 1.5l-2-.8-1.8 3.1 1.7 1.3a7.6 7.6 0 0 0 0 3l-1.7 1.3 1.8 3.1 2-.8a7.4 7.4 0 0 0 2.6 1.5l.4 2.4h3.6l.4-2.4a7.4 7.4 0 0 0 2.6-1.5l2 .8 1.8-3.1-1.7-1.3Z" />
            </svg>
          </button>
        </header>
        <nav aria-label="Main">
          <NavLink to="/overview">
            <Icon name="overview" />
            Overview
          </NavLink>
          <NavLink to="/proxies">
            <Icon name="proxies" />
            Proxies
          </NavLink>
          <NavLink to="/events">
            <Icon name="events" />
            Events
          </NavLink>
        </nav>
        <div className="sidebar__spacer" />
        <p className="sidebar__note">Checks run on your schedules. Open a proxy to inspect its evidence.</p>
      </aside>
      <main className="content" id="content">
        <Outlet />
      </main>
      {settingsOpen ? (
        <Dialog title="Settings" onClose={() => setSettingsOpen(false)} className="dialog--settings">
          <SettingsPage />
        </Dialog>
      ) : null}
    </div>
  );
}

export function App() {
  const { state } = useAuth();
  // Apply the saved theme on the login page too.
  useTheme();

  if (state.status === 'loading') {
    return (
      <p className="loading" role="status">
        Loading…
      </p>
    );
  }
  if (state.status === 'setup') {
    return <SetupPage />;
  }
  if (state.status === 'anonymous') {
    return <LoginPage />;
  }
  return (
    <Routes>
      <Route element={<Shell />}>
        <Route index element={<Navigate to="/overview" replace />} />
        <Route path="/overview" element={<OverviewPage />} />
        <Route path="/proxies" element={<ProxyPage />} />
        <Route path="/proxies/:id" element={<ProxyDetail />} />
        <Route path="/events" element={<EventPage />} />
        <Route path="/settings" element={<Navigate to="/proxies" replace />} />
        <Route path="*" element={<Navigate to="/overview" replace />} />
      </Route>
    </Routes>
  );
}
