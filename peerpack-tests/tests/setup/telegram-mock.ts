// tests/setup/telegram-mock.ts
// Мокируем Telegram WebApp API для тестов

import { Page } from '@playwright/test'

export const TEST_USER = {
  id: 155741924,
  first_name: 'Nick',
  last_name: 'Test',
  username: 'nick_test',
  language_code: 'ru',
}

export async function setupTelegramMock(page: Page, user = TEST_USER) {
  await page.addInitScript((u) => {
    // Мокируем весь Telegram.WebApp
    (window as any).Telegram = {
      WebApp: {
        initData: `user=${JSON.stringify(u)}&auth_date=${Date.now()}`,
        initDataUnsafe: {
          user: u,
          auth_date: Math.floor(Date.now() / 1000),
        },
        version: '6.9',
        platform: 'web',
        colorScheme: 'dark',
        themeParams: {
          bg_color: '#17212b',
          text_color: '#ffffff',
          hint_color: '#708499',
          link_color: '#5288c1',
          button_color: '#5288c1',
          button_text_color: '#ffffff',
          secondary_bg_color: '#232e3c',
        },
        isExpanded: true,
        viewportHeight: 812,
        viewportStableHeight: 812,
        ready: () => {},
        expand: () => {},
        close: () => {},
        MainButton: {
          text: '',
          color: '#5288c1',
          textColor: '#ffffff',
          isVisible: false,
          isActive: true,
          show: function() { this.isVisible = true },
          hide: function() { this.isVisible = false },
          setText: function(t: string) { this.text = t },
          onClick: () => {},
          offClick: () => {},
        },
        BackButton: {
          isVisible: false,
          show: function() { this.isVisible = true },
          hide: function() { this.isVisible = false },
          onClick: () => {},
          offClick: () => {},
        },
        HapticFeedback: {
          impactOccurred: () => {},
          notificationOccurred: () => {},
          selectionChanged: () => {},
        },
        CloudStorage: {
          setItem: (_key: string, _value: string, cb?: Function) => cb?.(null, true),
          getItem: (_key: string, cb?: Function) => cb?.(null, ''),
          removeItem: (_key: string, cb?: Function) => cb?.(null, true),
        },
        showAlert: (msg: string, cb?: Function) => { console.log('TG Alert:', msg); cb?.() },
        showConfirm: (_msg: string, cb?: Function) => cb?.(true),
      }
    }

    // CSS переменные Telegram темы
    const style = document.createElement('style')
    style.textContent = `
      :root {
        --tg-theme-bg-color: #17212b;
        --tg-theme-text-color: #ffffff;
        --tg-theme-hint-color: #708499;
        --tg-theme-link-color: #5288c1;
        --tg-theme-button-color: #5288c1;
        --tg-theme-button-text-color: #ffffff;
        --tg-theme-secondary-bg-color: #232e3c;
        --tg-viewport-height: 812px;
        --tg-viewport-stable-height: 812px;
      }
    `
    document.head.appendChild(style)
  }, user)
}
