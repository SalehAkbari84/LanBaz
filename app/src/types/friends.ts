/** Friend system wire types; mirror core/pkg/protocol/dto_friends.go. */

export type FriendState = 'pending_out' | 'pending_in' | 'friend'

export interface Friend {
  pub: string
  code: string
  name: string
  peer_id?: string
  state: FriendState
  trusted: boolean
  online: boolean
  last_seen?: string
  hosting?: boolean
  room_id?: string
  room_name?: string
  seats?: number
  game?: string
}

export interface FriendsList {
  code: string
  enabled: boolean
  friends: Friend[]
}

export interface FriendEvent {
  friend: Friend
  why: 'added' | 'request' | 'accepted' | 'declined' | 'removed' | 'trusted' | 'presence'
}

export interface JoinPrompt {
  id: string
  kind: 'request' | 'invite'
  friend: Friend
  room_id: string
  room_name?: string
}

export interface JoinStatus {
  friend: Friend
  room_id?: string
  status: 'sent' | 'invited' | 'joining' | 'connected' | 'rejected' | 'failed' | 'reconnecting'
  message?: string
}

export const FRIEND_EVENT = {
  update: 'friend.update',
  prompt: 'join.prompt',
  status: 'join.status',
} as const

/** network.kept: rooms reopened or rejoined automatically. */
export interface KeptNetworks {
  rooms: string[]
  hosting?: { name: string; mode?: string; members: number }
  hosts: { pub: string; name?: string; online: boolean; room_id?: string }[]
}
