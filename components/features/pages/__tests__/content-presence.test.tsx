import { render,screen,fireEvent,cleanup } from '@testing-library/react'
import { afterEach,it,expect,vi } from 'vitest'
import { ContentPresenceStatus } from '../content-presence'
import type { useContentPresence, PresenceMember } from '@/hooks/use-content-presence'
vi.mock('@/hooks/use-realtime',()=>({usePresenceTransportSafe:vi.fn(),useRealtimeChannelSafe:vi.fn(),useRealtimeEventSafe:vi.fn()}))
afterEach(cleanup)
const members:PresenceMember[]=[
 {connection_id:'a',user_id:'self',display_name:'Ada',revision:2,board_seq:null,cursor:null},
 {connection_id:'b',user_id:'other',display_name:'Alice',revision:1,board_seq:null,cursor:null},
 {connection_id:'c',user_id:'other',display_name:'Alice',revision:3,board_seq:null,cursor:null},
 {connection_id:'d',user_id:'unknown',revision:null,board_seq:null,cursor:null},
]
function presence():ReturnType<typeof useContentPresence>{return{allMembers:members,members:[members[0]],currentUserId:'self',setCursor:vi.fn(),available:true,confirmed:true,people:3,here:1}}
it('shows verified avatars and each human’s actual revisions, deduplicating tabs and preserving unknown positions',()=>{
 render(<ContentPresenceStatus presence={presence()} context={{kind:'artifact',pageId:'page',revision:2}}/>)
 fireEvent.click(screen.getByRole('button',{name:'Show people viewing this page'}))
 expect(screen.getByText('Ada (you)')).toBeInTheDocument()
 expect(screen.getAllByText('Alice')).toHaveLength(1)
 expect(screen.getByText('revisions 1, 3 · 2 tabs')).toBeInTheDocument()
 expect(screen.getByText('Viewer')).toBeInTheDocument()
 expect(screen.getByText('Joining…')).toBeInTheDocument()
 expect(screen.getAllByRole('listitem')).toHaveLength(3)
})
it('immediately removes participant names and the open popover when presence disconnects',()=>{
 const view=render(<ContentPresenceStatus presence={presence()} context={{kind:'artifact',pageId:'page',revision:2}}/>)
 fireEvent.click(screen.getByRole('button',{name:'Show people viewing this page'}))
 expect(screen.getByText('Alice')).toBeInTheDocument()
 view.rerender(<ContentPresenceStatus presence={{...presence(),available:false}} context={{kind:'artifact',pageId:'page',revision:2}}/>)
 expect(screen.queryByText('Alice')).toBeNull()
 expect(screen.queryByRole('list')).toBeNull()
 expect(screen.getByRole('status')).toHaveTextContent('Live presence unavailable')
})
