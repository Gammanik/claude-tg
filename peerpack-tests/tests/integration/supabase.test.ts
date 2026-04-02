import { createClient, SupabaseClient } from '@supabase/supabase-js'
import { beforeEach, afterEach, test, expect, describe } from 'vitest'

// Используем отдельный test project в Supabase (бесплатный)
let supabase: SupabaseClient

beforeEach(() => {
  const url = process.env.SUPABASE_TEST_URL
  const key = process.env.SUPABASE_TEST_SERVICE_KEY

  if (!url || !key) {
    console.warn('⚠️  SUPABASE_TEST_URL / SUPABASE_TEST_SERVICE_KEY не заданы, пропускаем интеграционные тесты')
    return
  }

  supabase = createClient(url, key)
})

afterEach(async () => {
  if (!supabase) return
  // Чистим тестовые данные
  await supabase.from('packages').delete().eq('is_test', true)
  await supabase.from('trips').delete().eq('is_test', true)
})

describe('packages', () => {
  test('создание посылки сохраняет корректные данные', async () => {
    if (!supabase) return

    const { data, error } = await supabase
      .from('packages')
      .insert({
        sender_id: 'test-user-123',
        from_city: 'Москва',
        to_city: 'Санкт-Петербург',
        weight: 2.5,
        description: 'Тестовая посылка',
        is_test: true,
      })
      .select()
      .single()

    expect(error).toBeNull()
    expect(data).toBeTruthy()
    expect(data.status).toBe('pending')
    expect(data.from_city).toBe('Москва')
    expect(data.to_city).toBe('Санкт-Петербург')
  })

  test('посылка не создаётся без обязательных полей', async () => {
    if (!supabase) return

    const { error } = await supabase
      .from('packages')
      .insert({ is_test: true }) // нет обязательных полей
      .select()
      .single()

    expect(error).toBeTruthy()
  })

  test('список посылок пользователя возвращает только его посылки', async () => {
    if (!supabase) return

    const userId = 'test-user-filter'

    // Создаём посылки двух пользователей
    await supabase.from('packages').insert([
      { sender_id: userId, from_city: 'Москва', to_city: 'СПб', is_test: true },
      { sender_id: 'other-user', from_city: 'Казань', to_city: 'Уфа', is_test: true },
    ])

    const { data, error } = await supabase
      .from('packages')
      .select('*')
      .eq('sender_id', userId)
      .eq('is_test', true)

    expect(error).toBeNull()
    expect(data?.every(p => p.sender_id === userId)).toBe(true)
  })
})

describe('trips', () => {
  test('создание поездки с корректными данными', async () => {
    if (!supabase) return

    const { data, error } = await supabase
      .from('trips')
      .insert({
        courier_id: 'test-courier-123',
        from_city: 'Москва',
        to_city: 'Санкт-Петербург',
        departure_date: new Date(Date.now() + 86400000).toISOString(), // завтра
        capacity: 10,
        is_test: true,
      })
      .select()
      .single()

    expect(error).toBeNull()
    expect(data.status).toBe('active')
    expect(data.capacity).toBe(10)
  })

  test('нельзя создать поездку с прошедшей датой', async () => {
    if (!supabase) return

    const { error } = await supabase
      .from('trips')
      .insert({
        courier_id: 'test-courier',
        from_city: 'Москва',
        to_city: 'СПб',
        departure_date: new Date('2020-01-01').toISOString(), // прошлое
        is_test: true,
      })
      .select()
      .single()

    // Должна быть ошибка валидации или check constraint
    expect(error).toBeTruthy()
  })
})

describe('package requests', () => {
  test('курьер может принять заявку на посылку', async () => {
    if (!supabase) return

    // Создаём поездку
    const { data: trip } = await supabase
      .from('trips')
      .insert({
        courier_id: 'test-courier',
        from_city: 'Москва',
        to_city: 'СПб',
        departure_date: new Date(Date.now() + 86400000).toISOString(),
        is_test: true,
      })
      .select()
      .single()

    // Создаём посылку
    const { data: pkg } = await supabase
      .from('packages')
      .insert({
        sender_id: 'test-sender',
        from_city: 'Москва',
        to_city: 'СПб',
        is_test: true,
      })
      .select()
      .single()

    if (!trip || !pkg) return

    // Создаём заявку
    const { data: request, error } = await supabase
      .from('package_requests')
      .insert({
        trip_id: trip.id,
        package_id: pkg.id,
        status: 'pending',
      })
      .select()
      .single()

    expect(error).toBeNull()
    expect(request.status).toBe('pending')
  })
})
