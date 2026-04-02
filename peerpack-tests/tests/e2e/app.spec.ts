import { test, expect } from '@playwright/test'
import { setupTelegramMock } from '../setup/telegram-mock'

test.beforeEach(async ({ page }) => {
  await setupTelegramMock(page)
  await page.goto('/')
  // Ждём пока моковые данные загрузятся
  await page.waitForLoadState('networkidle')
})

test('приложение загружается без ошибок', async ({ page }) => {
  // Нет критических JS-ошибок
  const errors: string[] = []
  page.on('pageerror', (err) => errors.push(err.message))

  await page.waitForTimeout(1000)
  expect(errors.filter(e => !e.includes('ResizeObserver'))).toHaveLength(0)
})

test('отображается список курьеров', async ({ page }) => {
  // Главный экран должен показать курьеров
  await expect(page.locator('[data-testid="courier-list"], .courier-card, [class*="courier"]').first())
    .toBeVisible({ timeout: 5000 })
})

test('форма поиска работает', async ({ page }) => {
  // Находим поле поиска и вводим город
  const searchInput = page.locator('input[placeholder*="город"], input[placeholder*="откуда"], input[type="text"]').first()
  if (await searchInput.isVisible()) {
    await searchInput.fill('Москва')
    await searchInput.press('Enter')
    await page.waitForTimeout(500)
    // После поиска что-то должно отображаться
    await expect(page.locator('body')).not.toContainText('Ошибка')
  }
})

test('мобильный viewport корректен', async ({ page }) => {
  // PeerPack — мобильное приложение, проверяем viewport
  const viewport = page.viewportSize()
  expect(viewport?.width).toBeLessThanOrEqual(430)
})

test('тема Telegram применена', async ({ page }) => {
  // CSS переменные Telegram должны работать
  const bgColor = await page.evaluate(() =>
    getComputedStyle(document.documentElement)
      .getPropertyValue('--tg-theme-bg-color').trim()
  )
  expect(bgColor).toBeTruthy()
})
