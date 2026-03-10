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
  const initialRef = useRef(value)
  const motionValue = useMotionValue(initialRef.current)
  const rounded = useTransform(motionValue, (v) => Math.round(v))

  useEffect(() => {
    const controls = animate(motionValue, value, {
      duration,
      ease: "easeOut",
    })
    return () => controls.stop()
  }, [value, duration, motionValue])

  return <motion.span className={cn(className)}>{rounded}</motion.span>
}
