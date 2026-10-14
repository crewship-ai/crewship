import { render,screen,fireEvent } from '@testing-library/react'
import { it,expect,vi } from 'vitest'
import { ArtifactCopyDialog } from '../artifact-copy-dialog'
it('requires explicit private copy and freezes original revision/payload on uncertain retry',()=>{
 const copy=vi.fn(),close=vi.fn()
 const props={revision:7,pending:false,uncertain:false,onCopy:copy,onClose:close}
 const view=render(<ArtifactCopyDialog {...props}/> )
 expect(copy).not.toHaveBeenCalled()
 fireEvent.change(screen.getByLabelText('Copy name'),{target:{value:'My report'}})
 fireEvent.change(screen.getByLabelText('Copy page address'),{target:{value:'my-report'}})
 fireEvent.click(screen.getByRole('button',{name:'Create private copy'}))
 expect(copy).toHaveBeenLastCalledWith({source_revision:7,name:'My report',slug:'my-report'})
 view.rerender(<ArtifactCopyDialog {...props} revision={8} uncertain error="Lost response"/> )
 expect(screen.getByLabelText('Copy name')).toBeDisabled()
 expect(screen.getByRole('button',{name:'Cancel'})).toBeDisabled()
 fireEvent.click(screen.getByRole('button',{name:'Retry same copy'}))
 expect(copy).toHaveBeenLastCalledWith({source_revision:7,name:'My report',slug:'my-report'})
 expect(close).not.toHaveBeenCalled()
})
