import { useState } from 'react';
import { NavLink, Navigate, Outlet, Route, Routes } from 'react-router-dom';
import { LoginPage } from '../features/auth/LoginPage';
import { SetupPage } from '../features/auth/SetupPage';
import { EventPage } from '../features/events/EventPage';
import { ProxyDetail } from '../features/proxies/ProxyDetail';
import { ProxyPage } from '../features/proxies/ProxyPage';
import { SettingsPage } from '../features/settings/SettingsPage';
import { Dialog } from '../components/Dialog';
import { useAuth } from './auth';
import { useTheme } from './theme';

function Shell() {
  const [settingsOpen, setSettingsOpen] = useState(false);
  return (
    <div className="app">
      <header className="topbar">
        <div className="brand">ProxyWard <span className="brand__version">v{__APP_VERSION__}</span></div>
        <nav aria-label="Main">
          <NavLink to="/proxies">Proxies</NavLink>
          <NavLink to="/events">Events</NavLink>
        </nav>
        <div className="topbar__actions">
          <button type="button" className="icon-button settings-button" aria-label="Settings" onClick={() => setSettingsOpen(true)}>
            <svg viewBox="0 0 24 24" aria-hidden="true" focusable="false">
              <path d="M12 8a4 4 0 1 0 0 8 4 4 0 0 0 0-8Z" />
              <path d="M19.4 13.5a7.6 7.6 0 0 0 0-3l1.7-1.3-1.8-3.1-2 .8a7.4 7.4 0 0 0-2.6-1.5L14.3 3h-3.6l-.4 2.4a7.4 7.4 0 0 0-2.6 1.5l-2-.8-1.8 3.1 1.7 1.3a7.6 7.6 0 0 0 0 3l-1.7 1.3 1.8 3.1 2-.8a7.4 7.4 0 0 0 2.6 1.5l.4 2.4h3.6l.4-2.4a7.4 7.4 0 0 0 2.6-1.5l2 .8 1.8-3.1-1.7-1.3Z" />
            </svg>
          </button>
        </div>
      </header>
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
        <Route index element={<Navigate to="/proxies" replace />} />
        <Route path="/proxies" element={<ProxyPage />} />
        <Route path="/proxies/:id" element={<ProxyDetail />} />
        <Route path="/events" element={<EventPage />} />
        <Route path="/settings" element={<Navigate to="/proxies" replace />} />
        <Route path="*" element={<Navigate to="/proxies" replace />} />
      </Route>
    </Routes>
  );
}
