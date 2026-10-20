import { NavLink, Navigate, Outlet, Route, Routes } from 'react-router-dom';
import { LoginPage } from '../features/auth/LoginPage';
import { SetupPage } from '../features/auth/SetupPage';
import { EventPage } from '../features/events/EventPage';
import { ProxyDetail } from '../features/proxies/ProxyDetail';
import { ProxyPage } from '../features/proxies/ProxyPage';
import { SettingsPage } from '../features/settings/SettingsPage';
import { useAuth } from './auth';
import { useTheme, type ThemeChoice } from './theme';

function Shell() {
  const { signOut } = useAuth();
  const [theme, setTheme] = useTheme();
  return (
    <div className="app">
      <header className="sidebar">
        <div className="brand">ProxyWard</div>
        <nav aria-label="Main">
          <NavLink to="/proxies">Proxies</NavLink>
          <NavLink to="/events">Events</NavLink>
          <NavLink to="/settings">Settings</NavLink>
        </nav>
        <div className="sidebar__foot">
          <label className="theme">
            <span>Theme</span>
            <select value={theme} onChange={(e) => setTheme(e.target.value as ThemeChoice)} aria-label="Theme">
              <option value="system">System</option>
              <option value="light">Light</option>
              <option value="dark">Dark</option>
            </select>
          </label>
          <button type="button" className="button button--small" onClick={() => void signOut()}>
            Sign out
          </button>
        </div>
      </header>
      <main className="content" id="content">
        <Outlet />
      </main>
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
        <Route path="/settings" element={<SettingsPage />} />
        <Route path="*" element={<Navigate to="/proxies" replace />} />
      </Route>
    </Routes>
  );
}
