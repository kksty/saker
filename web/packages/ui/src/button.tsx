import * as React from 'react'
import { Slot } from '@radix-ui/react-slot'
import { cva, type VariantProps } from 'class-variance-authority'
import { cn } from '@cyber/theme'

const buttonVariants = cva(
  'inline-flex select-none items-center justify-center whitespace-nowrap rounded-md text-ui font-medium ring-offset-background transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60 focus-visible:ring-offset-2 focus-visible:ring-offset-background disabled:pointer-events-none disabled:opacity-40',
  {
    variants: {
      variant: {
        // The only saturated action: one primary per view header.
        default: 'bg-primary-solid text-primary-foreground hover:bg-primary-solid-hover',
        destructive: 'bg-destructive text-destructive-foreground hover:bg-destructive/90',
        outline: 'border border-border bg-transparent text-ink hover:border-ink-3/40 hover:bg-surface-2',
        secondary: 'bg-secondary text-secondary-foreground hover:bg-accent',
        ghost: 'text-ink-2 hover:bg-surface-2 hover:text-ink',
        link: 'text-primary underline-offset-4 hover:underline',
        // Soft-tinted primary — the "recessed / launch" affordance used by toolbars
        // and rails across the deck (e.g. SessionList's local-agent launch button),
        // instead of hand-rolled `bg-primary/10 text-primary`.
        soft: 'bg-primary/12 text-primary hover:bg-primary/18',
      },
      size: {
        default: 'h-[var(--saker-control,36px)] gap-2 px-3.5 py-2',
        sm: 'h-[var(--saker-control-sm,32px)] gap-1.5 rounded-md px-3 text-ui',
        // Dense micro-action (rail/toolbar rows): shorter than `sm`, 11px text.
        // Replaces the hand-rolled `h-6 px-2 text-[10px]` buttons in SessionList
        // /App that had no matching Button size.
        xs: 'h-6 gap-1 rounded px-2 text-[11px]',
        lg: 'h-11 gap-2 rounded-lg px-5',
        icon: 'h-[var(--saker-control,36px)] w-[var(--saker-control,36px)]',
        'icon-sm': 'h-[var(--saker-control-sm,32px)] w-[var(--saker-control-sm,32px)]',
        'icon-xs': 'h-7 w-7',
      },
    },
    defaultVariants: {
      variant: 'default',
      size: 'default',
    },
  }
)

export interface ButtonProps
  extends React.ButtonHTMLAttributes<HTMLButtonElement>,
    VariantProps<typeof buttonVariants> {
  asChild?: boolean
  /**
   * Pressed / selected state for toggle-style buttons (toolbar tabs, rail items).
   * Applies the soft-primary tint over the chosen variant and sets `aria-pressed`,
   * so a `ghost` button reads as "active" without callers re-implementing it.
   */
  active?: boolean
}

const Button = React.forwardRef<HTMLButtonElement, ButtonProps>(
  ({ className, variant, size, active = false, asChild = false, ...props }, ref) => {
    const Comp = asChild ? Slot : 'button'
    return (
      <Comp
        className={cn(
          buttonVariants({ variant, size }),
          active && 'bg-primary/10 text-primary hover:bg-primary/10 hover:text-primary',
          className,
        )}
        ref={ref}
        aria-pressed={active || undefined}
        {...props}
      />
    )
  }
)
Button.displayName = 'Button'

export { Button, buttonVariants }
