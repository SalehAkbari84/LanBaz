import { create } from 'zustand'

export interface GameFirewall {
  game_id?: string
  game_name?: string
  path?: string
  blocked?: { name: string; display_name?: string }[]
}

/** The daemon's last firewall check of the running game (game.firewall). */
export const useGameFirewall = create<{ fw: GameFirewall | null }>(() => ({ fw: null }))
