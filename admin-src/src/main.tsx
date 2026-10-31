import React from 'react';
import { createRoot } from 'react-dom/client';
import App from './App';
import TelegramMiniApp from './TelegramMiniApp';
import { activateTelegramMiniApp, getSettings } from './lib/api';
import './styles/app.css';

const miniApp = getSettings().telegram_mini_app === true;
if (miniApp) activateTelegramMiniApp();
createRoot(document.getElementById('root')!).render(miniApp ? <TelegramMiniApp /> : <App />);
