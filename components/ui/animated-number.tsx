"use client"

import { useEffect, useRef } from "react"
import { motion, useMotionValue, useTransform, animate } from "motion/react"
import { cn } from "@/lib/utils"

interface AnimatedNumberProps {
  value: number
  duration?: number
  className?: string
}

export function AnimatedNumber({ value, duration = 0.5, className }: AnimatedNumberProps) {
  const motionValue = useMotionValue(0)
  const rounded = useTransform(motionValue, (v) => Math.round(v))
  const prevRef = useRef(value)

  useEffect(() => {
    const from = prevRef.current
    prevRef.current = value
    const controls = animate(motionValue, value, {
      duration: from === 0 && value === 0 ? 0 : duration,
      ease: "easeOut",
    })
    return () => controls.stop()
  }, [value, duration, motionValue])

  return <motion.span className={cn(className)}>{rounded}</motion.span>
}
